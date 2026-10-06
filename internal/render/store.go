package render

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tottemolive/internal/audio"
)

// Store はレンダリング済みのプレビューWAV(と、付随するバイナリ)を一時ファイルに保持し、HTTPで配信する。
// 長い曲のWAVをメモリに持たないよう、WAVは少しずつファイルへ書く(PreviewWAV)。
// 生PCMや大きな配列をバインディングの戻り値で返すとJSONが巨大になるので、フロントは
// /preview/{id}.wav のURLを <audio> に渡して再生する。Rangeリクエストに対応し、シークできる。
// 付随するバイナリ(PA出力の帯域レベル。小さい)は /preview/{id}.bands で配信する。
//
// 保持は最新 keep 件。古いものは、配信中の読み手がいなくなってから(Windowsは開いているファイルを消せない)ファイルを消す。
type Store struct {
	mu    sync.Mutex
	seq   int
	dir   string // 最初に要るときに作る
	items map[string]*item
	order []string
	keep  int
}

type item struct {
	wavPath string
	bands   []byte // nil なら無し
	refs    int    // 配信中の数
	dead    bool   // 保持の対象から外れた(参照が無くなったらファイルを消す)
}

// NewStore は最新 keep 件を保持するストアを返す(古いものから捨てる)。
func NewStore(keep int) *Store {
	return &Store{items: map[string]*item{}, keep: max(keep, 1)}
}

// tempDir は一時ファイルを置くディレクトリ(最初に呼ばれたときに作る)。
func (s *Store) tempDir() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tempDirLocked()
}

func (s *Store) tempDirLocked() (string, error) {
	if s.dir == "" {
		d, err := os.MkdirTemp("", previewTempPrefix+"*")
		if err != nil {
			return "", fmt.Errorf("一時フォルダを作れません: %w", err)
		}
		s.dir = d
	}
	return s.dir, nil
}

// PreviewWAV は、プレビューのWAV(16bit、ディザ付き)をファイルへ少しずつ書く。Sink を満たす。
// 書き終えたら Store.Commit、失敗・中断したら Abort。
type PreviewWAV struct {
	s    *Store
	f    *os.File
	w    *audio.WAV16Writer
	path string
}

// NewWAV は新しいプレビューWAVの書き込みを用意する(ファイルは、全体のフレーム数が分かる Start で作る)。
func (s *Store) NewWAV() *PreviewWAV { return &PreviewWAV{s: s} }

// Start は WAV を作り、ヘッダを書く(全体のフレーム数が分かっているので、先に書ける)。
func (w *PreviewWAV) Start(frames, sampleRate int) error {
	dir, err := w.s.tempDir()
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "preview-*.wav")
	if err != nil {
		return fmt.Errorf("一時ファイルを書けません: %w", err)
	}
	ww, err := audio.NewWAV16Writer(f, sampleRate, frames)
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	w.f, w.w, w.path = f, ww, f.Name()
	return nil
}

// Write は WAV の続きを書く。
func (w *PreviewWAV) Write(buf [][]float32) error { return w.w.Write(buf) }

// Abort は書きかけのファイルを消す。
func (w *PreviewWAV) Abort() {
	if w.f == nil {
		return
	}
	w.f.Close()
	os.Remove(w.path)
	w.f = nil
}

// Put はWAV(メモリ上。短い音の先行プレビュー用)を一時ファイルに書いて登録し、配信URLのパスを返す。
// 書けなかったときは空文字。
func (s *Store) Put(wav []byte) string {
	wavPath, _ := s.PutWithBands(wav, nil)
	return wavPath
}

// PutWithBands はWAV(メモリ上)と帯域レベルのバイナリ(無ければ nil)を登録し、それぞれの配信URLのパスを返す。
// bands が nil のときの bandsPath は空文字。書けなかったときは両方とも空文字。
func (s *Store) PutWithBands(wav, bands []byte) (wavPath, bandsPath string) {
	dir, err := s.tempDir()
	var path string
	if err == nil {
		var f *os.File
		if f, err = os.CreateTemp(dir, "preview-*.wav"); err == nil {
			path = f.Name()
			_, err = f.Write(wav)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
	}
	if err != nil {
		if path != "" {
			os.Remove(path)
		}
		return "", ""
	}
	return s.register(path, bands)
}

// Commit は書き終えた WAV と帯域レベルのバイナリ(無ければ nil)を登録し、それぞれの配信URLのパスを返す。
// bands が nil のときの bandsPath は空文字。
func (s *Store) Commit(w *PreviewWAV, bands []byte) (wavPath, bandsPath string, err error) {
	if w.f == nil {
		return "", "", fmt.Errorf("render: プレビューのWAVが書かれていません")
	}
	werr := w.w.Close()
	cerr := w.f.Close()
	w.f = nil
	if err := firstErr(werr, cerr); err != nil {
		os.Remove(w.path)
		return "", "", fmt.Errorf("一時ファイルを書けません: %w", err)
	}
	wavPath, bandsPath = s.register(w.path, bands)
	return wavPath, bandsPath, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// register は書き終えたWAVのファイルを項目として登録し、古いものを保持の対象から外す。
func (s *Store) register(wavFile string, bands []byte) (wavPath, bandsPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	s.items[id] = &item{wavPath: wavFile, bands: bands}
	s.order = append(s.order, id)
	for len(s.order) > s.keep {
		s.killLocked(s.order[0])
		s.order = s.order[1:]
	}
	wavPath = "/preview/" + id + ".wav"
	if bands != nil {
		bandsPath = "/preview/" + id + ".bands"
	}
	return wavPath, bandsPath
}

// killLocked は項目を保持の対象から外す。配信中でなければ、すぐファイルを消す。
func (s *Store) killLocked(id string) {
	it, ok := s.items[id]
	if !ok {
		return
	}
	delete(s.items, id)
	it.dead = true
	if it.refs == 0 {
		os.Remove(it.wavPath)
	}
}

// Close は保持しているファイルと一時ディレクトリを消す。アプリの終了時に呼ぶ。
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.items {
		s.killLocked(id)
	}
	s.order = nil
	if s.dir != "" {
		os.RemoveAll(s.dir) // 配信中のファイルが残っていても、終了時なので構わない(消せなければ次回の起動時の掃除で消える)
		s.dir = ""
	}
}

// ServeHTTP は /preview/{id}.wav と /preview/{id}.bands を配信する。それ以外のパスは 404。
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/preview/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	var id, contentType string
	var isWav bool
	if base, found := strings.CutSuffix(name, ".wav"); found {
		id, contentType, isWav = base, "audio/wav", true
	} else if base, found := strings.CutSuffix(name, ".bands"); found {
		id, contentType = base, "application/octet-stream"
	} else {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	it, found := s.items[id]
	if found {
		it.refs++ // 配信している間は、ファイルを消さない
	}
	s.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	defer s.unref(it)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	if !isWav {
		if it.bands == nil {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(it.bands))
		return
	}
	f, err := os.Open(it.wavPath)
	if err != nil {
		http.Error(w, "preview file unavailable", http.StatusInternalServerError)
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(it.wavPath), time.Time{}, f)
}

// unref は配信を終えた項目の参照を返す。保持の対象から外れていて、参照が無くなったらファイルを消す
// (ファイルを閉じてから呼ばれる)。
func (s *Store) unref(it *item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it.refs--
	if it.dead && it.refs == 0 {
		os.Remove(it.wavPath)
	}
}
