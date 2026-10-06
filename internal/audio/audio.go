// Package audio はffmpeg/ffprobeを子プロセスとして呼び、デコード(生PCM受け取り)とWAVエンコードを行う。
// 形式ごとの純Goデコーダは持たない。ffmpegがPATH上に必要(環境変数 TOTTEMOLIVE_FFMPEG / TOTTEMOLIVE_FFPROBE で上書き可)。
package audio

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"tottemolive/internal/procutil"
)

// Channels は内部表現のチャンネル数(ステレオ固定)。
const Channels = 2

func bin(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// Available はffmpegが実行できるかを返す。
func Available() bool {
	_, err := exec.LookPath(bin("TOTTEMOLIVE_FFMPEG", "ffmpeg"))
	return err == nil
}

// Info は音源ファイルの情報。
type Info struct {
	DurationSec float64 `json:"durationSec"`
	SampleRate  int     `json:"sampleRate"`
	Channels    int     `json:"channels"`
}

// probeCache は Probe の結果を、ファイルの同一性(パス・サイズ・更新時刻)ごとに保存する。
// ffprobe の起動は、デコードのたびに走ると遅く、コンソール窓の件でも不利なので、1ファイル1回にする。
var (
	probeMu    sync.Mutex
	probeCache = map[string]Info{}
	probeRuns  atomic.Int64 // ffprobe を実際に起動した回数(テスト用)
)

func probeKey(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano()), true
}

// Probe はffprobeで長さ・サンプルレート・チャンネル数を取得する。同じファイル(中身が同じ)は結果を再利用する。
func Probe(ctx context.Context, path string) (Info, error) {
	key, ok := probeKey(path)
	if ok {
		probeMu.Lock()
		info, hit := probeCache[key]
		probeMu.Unlock()
		if hit {
			return info, nil
		}
	}
	info, err := runProbe(ctx, path)
	if err == nil && ok {
		probeMu.Lock()
		probeCache[key] = info
		probeMu.Unlock()
	}
	return info, err
}

func runProbe(ctx context.Context, path string) (Info, error) {
	probeRuns.Add(1)
	cmd := exec.CommandContext(ctx, bin("TOTTEMOLIVE_FFPROBE", "ffprobe"),
		"-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels:format=duration",
		"-of", "json", path)
	procutil.HideConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	var r struct {
		Streams []struct {
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &r); err != nil || len(r.Streams) == 0 {
		return Info{}, fmt.Errorf("ffprobe %s: no audio stream", path)
	}
	sr, _ := strconv.Atoi(r.Streams[0].SampleRate)
	dur, _ := strconv.ParseFloat(r.Format.Duration, 64)
	return Info{DurationSec: dur, SampleRate: sr, Channels: r.Streams[0].Channels}, nil
}

// Decoder は音源を sr Hz のfloat32にデコードしながら、少しずつ読み出す(リサンプル・チャンネル変換はffmpeg任せ)。
// ffmpegの出力(生PCM)をパイプで受け取るので、曲全体をメモリに持たない。
type Decoder struct {
	ctx      context.Context
	path     string
	cmd      *exec.Cmd
	stdout   io.ReadCloser
	stderr   *bytes.Buffer
	channels int
	expected int // 想定フレーム数(長さが分からなければ 0)
	progress func(float64)

	raw      []byte
	pos      int // raw の未変換の先頭
	have     int // raw の有効なバイト数
	frames   int // 読み出したフレーム数
	eof      bool
	finished bool  // Wait まで済んだ
	err      error // 終わったときの結果(io.EOF か、失敗)
}

// OpenDecoder は音源をステレオでデコードするデコーダを開く。progress は 0〜1(長さが分かる場合のみ、概算)。
func OpenDecoder(ctx context.Context, path string, sr int, progress func(ratio float64)) (*Decoder, error) {
	return openDecoder(ctx, path, sr, Channels, progress)
}

func openDecoder(ctx context.Context, path string, sr, channels int, progress func(float64)) (*Decoder, error) {
	var expect float64
	if info, err := Probe(ctx, path); err == nil {
		expect = info.DurationSec * float64(sr)
	}
	args := []string{"-v", "error", "-nostdin", "-i", path, "-vn",
		"-f", "f32le", "-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(sr), "pipe:1"}
	cmd := exec.CommandContext(ctx, bin("TOTTEMOLIVE_FFMPEG", "ffmpeg"), args...)
	procutil.HideConsole(cmd)
	d := &Decoder{ctx: ctx, path: path, cmd: cmd, stderr: &bytes.Buffer{}, channels: channels,
		expected: int(expect), progress: progress, raw: make([]byte, 4*channels*8192)}
	cmd.Stderr = d.stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	d.stdout = stdout
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg を起動できません: %w", err)
	}
	return d, nil
}

// ExpectedFrames は、ファイルの長さから見積もった総フレーム数(長さが分からなければ 0)。
func (d *Decoder) ExpectedFrames() int { return d.expected }

// Read は dst(チャンネル別、全チャンネル同じ長さ)を満たすまで読み、読めたフレーム数を返す。
// 終わりに達して1フレームも読めなかったときは 0, io.EOF。デコードに失敗したときはそのエラー
// (キャンセルされていれば ctx.Err())を返す。
func (d *Decoder) Read(dst [][]float32) (int, error) {
	want := len(dst[0])
	fb := 4 * d.channels
	n := 0
	for n < want {
		if d.have-d.pos < fb {
			if d.eof { // 残りが1フレームに満たないバイトは捨てる
				break
			}
			d.have = copy(d.raw, d.raw[d.pos:d.have])
			d.pos = 0
			m, err := d.stdout.Read(d.raw[d.have:])
			d.have += m
			if err != nil {
				d.eof = true
			}
			continue
		}
		k := min((d.have-d.pos)/fb, want-n)
		for i := 0; i < k; i++ {
			for c := 0; c < d.channels; c++ {
				bits := binary.LittleEndian.Uint32(d.raw[d.pos+i*fb+4*c:])
				dst[c][n+i] = math.Float32frombits(bits)
			}
		}
		d.pos += k * fb
		n += k
	}
	d.frames += n
	if d.progress != nil && d.expected > 0 && n > 0 {
		d.progress(math.Min(float64(d.frames)/float64(d.expected), 1))
	}
	if n > 0 {
		return n, nil
	}
	return 0, d.finish()
}

// finish はffmpegの終了を待ち、結果(正常なら io.EOF)を返す。
func (d *Decoder) finish() error {
	if d.finished {
		return d.err
	}
	d.finished = true
	if err := d.cmd.Wait(); err != nil {
		if cerr := d.ctx.Err(); cerr != nil {
			d.err = cerr
		} else {
			d.err = fmt.Errorf("ffmpeg decode %s: %w: %s", d.path, err, strings.TrimSpace(d.stderr.String()))
		}
		return d.err
	}
	if cerr := d.ctx.Err(); cerr != nil {
		d.err = cerr
	} else if d.frames == 0 {
		d.err = fmt.Errorf("ffmpeg decode %s: 音声データがありません", d.path)
	} else {
		d.err = io.EOF
	}
	return d.err
}

// Close はデコーダを閉じる。終わっていなければffmpegを止める。
func (d *Decoder) Close() error {
	if !d.finished {
		d.finished = true
		d.err = io.ErrClosedPipe
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
	}
	return nil
}

// Decode は音源全体を sr Hz のステレオfloat32にデコードする(リサンプル・チャンネル変換はffmpeg任せ)。
// progress は 0〜1(長さが分かる場合のみ、概算)。
func Decode(ctx context.Context, path string, sr int, progress func(ratio float64)) ([][]float32, error) {
	d, err := OpenDecoder(ctx, path, sr, progress)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	out := make([][]float32, Channels)
	if d.expected > 0 {
		// 長さが分かっているときは事前に確保する(appendの倍々の再確保で、曲の2〜3倍ぶんのメモリを一時的に使わないため)
		for c := range out {
			out[c] = make([]float32, 0, d.expected+sr)
		}
	}
	buf := [][]float32{make([]float32, 8192), make([]float32, 8192)}
	for {
		n, err := d.Read(buf)
		for c := range out {
			out[c] = append(out[c], buf[c][:n]...)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// peakSampleRate は波形表示用のモノラルデコードのサンプルレート。
const peakSampleRate = 8000

// Peaks は波形表示用のmin/maxピーク列(長さ 2*width、[min0, max0, min1, max1, ...])を返す。
// 曲全体を持たず、流し読みしながらブロックごとのmin/maxだけを保持する(ブロック数は幅の8倍ほど)。
// 列は、そのブロックを幅の数に集約したもの。
func Peaks(ctx context.Context, path string, width int) ([]float32, error) {
	if width <= 0 {
		return nil, fmt.Errorf("audio: width must be positive")
	}
	d, err := openDecoder(ctx, path, peakSampleRate, 1, nil)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	blockLen := max(d.expected/(8*width), 1)
	var mins, maxs []float32
	lo, hi := float32(0), float32(0)
	inBlock := 0
	buf := [][]float32{make([]float32, 8192)}
	for {
		n, err := d.Read(buf)
		for _, v := range buf[0][:n] {
			lo, hi = min(lo, v), max(hi, v)
			if inBlock++; inBlock == blockLen {
				mins, maxs = append(mins, lo), append(maxs, hi)
				lo, hi, inBlock = 0, 0, 0
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if inBlock > 0 {
		mins, maxs = append(mins, lo), append(maxs, hi)
	}
	nb := len(mins)
	out := make([]float32, 2*width)
	for i := 0; i < width; i++ {
		from, to := nb*i/width, nb*(i+1)/width
		if to <= from {
			to = min(from+1, nb)
		}
		lo, hi := float32(0), float32(0)
		for b := from; b < to; b++ {
			lo, hi = min(lo, mins[b]), max(hi, maxs[b])
		}
		out[2*i], out[2*i+1] = lo, hi
	}
	return out, nil
}

// WAVEncoder はステレオfloat32を24bit WAVとして、少しずつ書き出す(ffmpegへ生PCMをパイプ)。
type WAVEncoder struct {
	ctx    context.Context
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr *bytes.Buffer
	b      []byte
	done   bool
}

// NewWAVEncoder は path への24bit WAV書き出しを始める。
func NewWAVEncoder(ctx context.Context, path string, sr int) (*WAVEncoder, error) {
	cmd := exec.CommandContext(ctx, bin("TOTTEMOLIVE_FFMPEG", "ffmpeg"),
		"-v", "error", "-y", "-f", "f32le", "-ar", strconv.Itoa(sr), "-ac", strconv.Itoa(Channels),
		"-i", "pipe:0", "-c:a", "pcm_s24le", "-f", "wav", path)
	procutil.HideConsole(cmd)
	e := &WAVEncoder{ctx: ctx, cmd: cmd, stderr: &bytes.Buffer{}}
	cmd.Stderr = e.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	e.stdin = stdin
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg を起動できません: %w", err)
	}
	return e, nil
}

// Write は buf(ステレオ、チャンネル別)の続きを書く。
func (e *WAVEncoder) Write(buf [][]float32) error {
	if len(buf) != Channels {
		return fmt.Errorf("audio: stereo buffer required")
	}
	n := len(buf[0])
	e.b = e.b[:0]
	for i := 0; i < n; i++ {
		for c := 0; c < Channels; c++ {
			e.b = binary.LittleEndian.AppendUint32(e.b, math.Float32bits(buf[c][i]))
		}
	}
	if _, err := e.stdin.Write(e.b); err != nil {
		if cerr := e.ctx.Err(); cerr != nil {
			return cerr
		}
		return err
	}
	return nil
}

// Close は書き込みを終えて、ffmpegの終了を待つ。
func (e *WAVEncoder) Close() error {
	if e.done {
		return nil
	}
	e.done = true
	e.stdin.Close()
	if err := e.cmd.Wait(); err != nil {
		if cerr := e.ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("ffmpeg encode: %w: %s", err, strings.TrimSpace(e.stderr.String()))
	}
	return nil
}

// Abort はffmpegを止める(出力ファイルは呼び出し側が消す)。
func (e *WAVEncoder) Abort() {
	if e.done {
		return
	}
	e.done = true
	_ = e.cmd.Process.Kill()
	e.stdin.Close()
	_ = e.cmd.Wait()
}

// EncodeWAV はステレオfloat32を24bit WAVとして書き出す(ffmpegへ生PCMをパイプ)。
func EncodeWAV(ctx context.Context, path string, buf [][]float32, sr int, progress func(ratio float64)) error {
	if len(buf) != Channels {
		return fmt.Errorf("audio: stereo buffer required")
	}
	e, err := NewWAVEncoder(ctx, path, sr)
	if err != nil {
		return err
	}
	const frames = 8192
	n := len(buf[0])
	var werr error
	for start := 0; start < n && werr == nil; start += frames {
		end := min(start+frames, n)
		werr = e.Write([][]float32{buf[0][start:end], buf[1][start:end]})
		if progress != nil {
			progress(float64(end) / float64(n))
		}
	}
	if err := e.Close(); err != nil {
		return err
	}
	return werr
}

// WAV16Writer はステレオfloat32を16bit PCMのWAVとして、少しずつ書く。総フレーム数が分かっているので、
// ヘッダを先に書ける(Seek不要)。量子化ノイズを散らすためTPDFディザを掛ける(乱数は固定シードで再現可能)。
type WAV16Writer struct {
	w       *bufio.Writer
	rng     *rand.Rand
	frames  int
	written int
	b       []byte
}

// NewWAV16Writer は frames フレームぶんの16bit WAVを w へ書き始める(ヘッダを書く)。
func NewWAV16Writer(w io.Writer, sr, frames int) (*WAV16Writer, error) {
	dataLen := frames * 2 * Channels
	b := make([]byte, 0, 44)
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+dataLen))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, Channels)
	b = binary.LittleEndian.AppendUint32(b, uint32(sr))
	b = binary.LittleEndian.AppendUint32(b, uint32(sr*2*Channels))
	b = binary.LittleEndian.AppendUint16(b, 2*Channels)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(dataLen))
	bw := bufio.NewWriterSize(w, 1<<20)
	if _, err := bw.Write(b); err != nil {
		return nil, err
	}
	return &WAV16Writer{w: bw, rng: rand.New(rand.NewSource(1)), frames: frames}, nil
}

// Write は buf(ステレオ、チャンネル別)の続きを書く。
func (w *WAV16Writer) Write(buf [][]float32) error {
	n := len(buf[0])
	w.b = w.b[:0]
	for i := 0; i < n; i++ {
		for c := 0; c < Channels; c++ {
			v := float64(buf[c][i])*32767 + (w.rng.Float64() - w.rng.Float64())
			v = math.Max(-32768, math.Min(32767, math.Round(v)))
			w.b = binary.LittleEndian.AppendUint16(w.b, uint16(int16(v)))
		}
	}
	w.written += n
	_, err := w.w.Write(w.b)
	return err
}

// Close は書き残しを出力する。書いたフレーム数が宣言と違えばエラー。
func (w *WAV16Writer) Close() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	if w.written != w.frames {
		return fmt.Errorf("audio: WAV16 に %d フレーム書いたが、宣言は %d フレーム", w.written, w.frames)
	}
	return nil
}

// WAV16 はステレオfloat32を16bit PCMのWAV(メモリ上)にする。短い音(先行プレビューの窓)用。
func WAV16(buf [][]float32, sr int) []byte {
	var out bytes.Buffer
	w, _ := NewWAV16Writer(&out, sr, len(buf[0]))
	_ = w.Write(buf)
	_ = w.Close()
	return out.Bytes()
}
