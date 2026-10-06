package render

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// 段の出力(PA・直接音・残響)は、メモリではなく一時ファイルのスプールに置く。
// 形式は float32 リトルエンディアンの交互並び(f32le、フレーム × チャンネル)の生PCM
// (float32のまま持つので、プレビューと書き出しの音が一致する)。

const spoolBufSize = 1 << 20

// spoolMeta はスプールに付けて持つ小さな値。
type spoolMeta struct {
	SongLen int     // 曲の長さ(バスのフレーム数)
	BusLufs float64 // PA段のスプール(バス)の統合ラウドネス
}

// spool は一時ファイルに書き終えた段の出力。参照カウントで寿命を管理する
// (キャッシュが1つ持ち、使う側が使っている間だけ1つずつ持つ。0になると削除する)。
// Windowsでは、開いているファイルは消せないので、読み手を閉じてから release すること。
type spool struct {
	path   string
	frames int
	ch     int
	meta   spoolMeta

	mu   sync.Mutex
	refs int
	// lost はファイルが(他から)消されていると分かったときに立てる。キャッシュはこのスプールを使わず、外す。
	lost atomic.Bool
}

// errSpoolLost はスプールのファイルが消えていて読めないことを表す。キャッシュに当たった段なら、
// 外して再計算すれば直るので、RenderTo / PreviewWindow は1回だけやり直す。
var errSpoolLost = errors.New("render: 一時ファイルが消えています")

// missing はファイルが消えているか(使う前に確かめる。消えていれば lost を立てる)。
func (s *spool) missing() bool {
	if s.lost.Load() {
		return true
	}
	if _, err := os.Stat(s.path); errors.Is(err, fs.ErrNotExist) {
		s.lost.Store(true)
		return true
	}
	return false
}

// openFile はスプールのファイルを開く。消えていれば lost を立てて errSpoolLost を返す。
func (s *spool) openFile() (*os.File, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.lost.Store(true)
			return nil, fmt.Errorf("%w: %v", errSpoolLost, err)
		}
		return nil, fmt.Errorf("一時ファイルを読めません: %w", err)
	}
	return f, nil
}

// acquire は参照を1つ取る。すでに解放済み(削除済み)なら false。
func (s *spool) acquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refs <= 0 {
		return false
	}
	s.refs++
	return true
}

// release は参照を1つ返す。0になったらファイルを削除する。
func (s *spool) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refs <= 0 {
		return
	}
	if s.refs--; s.refs == 0 {
		os.Remove(s.path)
	}
}

// spoolReader は、スプールを先頭(または途中)から順に読む。開いている間は参照を持つ。
type spoolReader struct {
	s  *spool
	f  *os.File
	br *bufio.Reader
	b  []byte
}

// open は frame フレーム目から順に読むリーダーを開く。Close するまで参照を持つ。
func (s *spool) open(from int) (*spoolReader, error) {
	if !s.acquire() {
		return nil, errors.New("render: 一時ファイルはすでに解放されています")
	}
	f, err := s.openFile()
	if err != nil {
		s.release()
		return nil, err
	}
	if from > 0 {
		if _, err := f.Seek(int64(from)*int64(s.ch)*4, io.SeekStart); err != nil {
			f.Close()
			s.release()
			return nil, err
		}
	}
	return &spoolReader{s: s, f: f, br: bufio.NewReaderSize(f, spoolBufSize)}, nil
}

// Read は dst(チャンネル別、全チャンネル同じ長さ)を満たすまで読む。終わりに達して1フレームも読めなければ 0, io.EOF。
func (r *spoolReader) Read(dst [][]float32) (int, error) {
	want := len(dst[0])
	fb := 4 * r.s.ch
	if cap(r.b) < want*fb {
		r.b = make([]byte, want*fb)
	}
	b := r.b[:want*fb]
	n, err := io.ReadFull(r.br, b)
	frames := n / fb
	for i := 0; i < frames; i++ {
		for c := 0; c < r.s.ch; c++ {
			dst[c][i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*fb+4*c:]))
		}
	}
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil
		if frames == 0 {
			return 0, io.EOF
		}
	}
	return frames, err
}

// Close はファイルを閉じてから参照を返す(この順でないと、Windowsでは最後の参照の削除に失敗する)。
func (r *spoolReader) Close() error {
	err := r.f.Close()
	r.s.release()
	return err
}

// ReadRange は [from, to) を読む。範囲の外(負の位置・ファイルの終わりの先)は無音。
func (s *spool) ReadRange(from, to int) ([][]float32, error) {
	n := max(to-from, 0)
	out := make([][]float32, s.ch)
	for c := range out {
		out[c] = make([]float32, n)
	}
	lo, hi := max(from, 0), min(to, s.frames)
	if hi <= lo {
		return out, nil
	}
	if !s.acquire() {
		return nil, errors.New("render: 一時ファイルはすでに解放されています")
	}
	defer s.release()
	f, err := s.openFile()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fb := 4 * s.ch
	b := make([]byte, (hi-lo)*fb)
	if _, err := f.ReadAt(b, int64(lo)*int64(fb)); err != nil {
		return nil, fmt.Errorf("一時ファイルを読めません: %w", err)
	}
	for i := 0; i < hi-lo; i++ {
		for c := 0; c < s.ch; c++ {
			out[c][lo-from+i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*fb+4*c:]))
		}
	}
	return out, nil
}

// spoolWriter はスプールを書く。書き終えたら Commit(成功)か Abort(失敗・中断。書きかけを消す)。
type spoolWriter struct {
	f      *os.File
	bw     *bufio.Writer
	path   string
	ch     int
	frames int
	b      []byte
}

// newSpoolWriter は dir に新しいスプールファイルを作る。
func newSpoolWriter(dir string, ch int) (*spoolWriter, error) {
	f, err := os.CreateTemp(dir, "spool-*.f32")
	if err != nil {
		return nil, fmt.Errorf("一時ファイルを書けません: %w", err)
	}
	return &spoolWriter{f: f, bw: bufio.NewWriterSize(f, spoolBufSize), path: f.Name(), ch: ch}, nil
}

// Write は buf(チャンネル別)の続きを書く。
func (w *spoolWriter) Write(buf [][]float32) error {
	n := len(buf[0])
	w.b = w.b[:0]
	for i := 0; i < n; i++ {
		for c := 0; c < w.ch; c++ {
			w.b = binary.LittleEndian.AppendUint32(w.b, math.Float32bits(buf[c][i]))
		}
	}
	if _, err := w.bw.Write(w.b); err != nil {
		return fmt.Errorf("一時ファイルを書けません: %w", err)
	}
	w.frames += n
	return nil
}

// Commit は書き終えて、スプールを返す(参照は1つ。呼び出し側(キャッシュなど)が持つ)。
func (w *spoolWriter) Commit(meta spoolMeta) (*spool, error) {
	err := w.bw.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(w.path)
		return nil, fmt.Errorf("一時ファイルを書けません: %w", err)
	}
	return &spool{path: w.path, frames: w.frames, ch: w.ch, meta: meta, refs: 1}, nil
}

// Abort は書きかけのファイルを消す。
func (w *spoolWriter) Abort() {
	w.f.Close()
	os.Remove(w.path)
}

// lookupSpool はスロット slot のスプールが key と一致すれば、参照を1つ取って返す(呼び出し側が release する)。
// 一致しなければ、古いエントリを外し(計算前に捨てて、新旧が同時にディスクに載らないようにする)、false。
// c が nil なら常に false。
func (c *cache) lookupSpool(slot, key string) (*spool, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.slots[slot]; ok {
		if s, isSpool := e.val.(*spool); isSpool && e.key == key && !s.missing() && s.acquire() {
			st := c.stat[slot]
			st.Hits++
			c.stat[slot] = st
			return s, true
		}
		c.dropLocked(slot)
	}
	return nil, false
}

// putSpool は新しく計算したスプールをスロットに登録する。キャッシュが、Commit で得た参照を引き継ぐ。
// 呼び出し側が使うときは、putSpool の前に acquire すること(登録後に別の実行が置き換えると、消えてしまう)。
func (c *cache) putSpool(slot, key string, s *spool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(slot)
	c.slots[slot] = cacheEntry{key: key, val: s}
	st := c.stat[slot]
	st.Computed++
	c.stat[slot] = st
}

// dropSlot はスロットのエントリを外す(スプールならキャッシュの参照を返す)。
func (c *cache) dropSlot(slot string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(slot)
}

func (c *cache) dropLocked(slot string) {
	if e, ok := c.slots[slot]; ok {
		delete(c.slots, slot)
		if s, isSpool := e.val.(*spool); isSpool {
			s.release()
		}
	}
}

// spoolBytes は保持しているスプールのファイルサイズの合計。
func (c *cache) spoolBytes() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var n int64
	for _, e := range c.slots {
		if s, ok := e.val.(*spool); ok {
			n += int64(s.frames) * int64(s.ch) * 4
		}
	}
	return n
}

// closeAll はすべてのエントリを外す(スプールの参照を返す)。
func (c *cache) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for slot := range c.slots {
		c.dropLocked(slot)
	}
}

// 一時ディレクトリの名前の頭。
const (
	renderTempPrefix  = "tottemolive-render-"
	previewTempPrefix = "tottemolive-preview-"
)

// staleTempName は掃除の対象になる作業ディレクトリの名前(MkdirTemp の乱数部は数字)。
// ユーザー指定のフォルダに置かれた、無関係な「tottemolive-render-メモ」などを消さないよう、厳密に照合する。
var staleTempName = regexp.MustCompile(`^tottemolive-(render|preview)-\d+$`)

// touchWorkdir は作業ディレクトリの更新時刻を現在にする(使っている間の心拍)。実行中のインスタンスは、
// 作業ディレクトリを使う(tempDir)たびに呼ぶので、別のインスタンスの起動時の掃除(CleanStaleTempIn)が、
// 使用中のものを「古い」と見て消すことはない。失敗しても処理は続ける。
func touchWorkdir(dir string) {
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
}

// CleanStaleTemp は、異常終了などで残った一時ディレクトリ(tottemolive-render-<数字> / tottemolive-preview-<数字>)のうち、
// maxAge より長く使われていないものを消す。起動時に呼ぶ。
func CleanStaleTemp(maxAge time.Duration) { CleanStaleTempIn("", maxAge) }

// CleanStaleTempIn は CleanStaleTemp の、掃除する場所(base。空なら OS の一時ディレクトリ)を指定できる版。
// キャッシュの置き場所の設定を変えても、前の置き場所に残ったものを掃除できるよう、起動時は両方に対して呼ぶ。
//
// 消す対象は、名前が厳密に tottemolive-(render|preview)-<数字> に一致するディレクトリで、更新時刻が maxAge より古いもの。
// 更新時刻は、実行中のインスタンスが使うたびに更新する(touchWorkdir)ので、使用中のものは消えない。
// (目印ファイルは置かない: 作業ディレクトリの中身はスプールだけという前提を保ち、旧版が作ったものも同じ基準で扱える)
func CleanStaleTempIn(base string, maxAge time.Duration) {
	if base == "" {
		base = os.TempDir()
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !staleTempName.MatchString(name) {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > maxAge {
			os.RemoveAll(filepath.Join(base, name))
		}
	}
}
