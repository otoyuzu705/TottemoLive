package render

import (
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestSpool(t *testing.T, dir string, frames, ch int) (*spool, [][]float32) {
	t.Helper()
	rng := rand.New(rand.NewSource(71))
	data := make([][]float32, ch)
	for c := range data {
		data[c] = make([]float32, frames)
		for i := range data[c] {
			data[c][i] = rng.Float32()*2 - 1
		}
	}
	w, err := newSpoolWriter(dir, ch)
	if err != nil {
		t.Fatal(err)
	}
	for from := 0; from < frames; from += 3333 {
		end := min(from+3333, frames)
		part := make([][]float32, ch)
		for c := range part {
			part[c] = data[c][from:end]
		}
		if err := w.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	s, err := w.Commit(spoolMeta{SongLen: frames, BusLufs: -14.5})
	if err != nil {
		t.Fatal(err)
	}
	return s, data
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSpoolWriteReadAndRange(t *testing.T) {
	dir := t.TempDir()
	s, data := writeTestSpool(t, dir, 10000, 2)
	if s.frames != 10000 || s.ch != 2 || s.meta.SongLen != 10000 || s.meta.BusLufs != -14.5 {
		t.Fatalf("spool %+v", s)
	}
	// 先頭から順に(細かく・大きく)読む
	for _, size := range []int{1, 777, 20000} {
		r, err := s.open(0)
		if err != nil {
			t.Fatal(err)
		}
		got := [][]float32{nil, nil}
		buf := [][]float32{make([]float32, size), make([]float32, size)}
		for count := 0; ; count++ {
			n, err := r.Read(buf)
			for c := range got {
				got[c] = append(got[c], buf[c][:n]...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if size == 1 && count > 3000 {
				break
			}
		}
		r.Close()
		for c := range got {
			for i := range got[c] {
				if got[c][i] != data[c][i] {
					t.Fatalf("size %d: differs at ch%d[%d]", size, c, i)
				}
			}
		}
		if size != 1 && len(got[0]) != 10000 {
			t.Fatalf("size %d: read %d frames", size, len(got[0]))
		}
	}
	// 途中から読む
	r, _ := s.open(9000)
	buf := [][]float32{make([]float32, 5000), make([]float32, 5000)}
	n, _ := r.Read(buf)
	r.Close()
	if n != 1000 || buf[0][0] != data[0][9000] || buf[1][999] != data[1][9999] {
		t.Errorf("open(9000): n=%d", n)
	}
	// ReadRange: 範囲の外はゼロ
	got, err := s.ReadRange(-5, 10005)
	if err != nil {
		t.Fatal(err)
	}
	for c := range got {
		if len(got[c]) != 10010 {
			t.Fatalf("range length %d", len(got[c]))
		}
		for i, v := range got[c] {
			var want float32
			if j := i - 5; j >= 0 && j < 10000 {
				want = data[c][j]
			}
			if v != want {
				t.Fatalf("range ch%d[%d]=%v want %v", c, i, v, want)
			}
		}
	}
	if got, _ := s.ReadRange(20000, 20010); len(got[0]) != 10 || got[0][3] != 0 {
		t.Error("range entirely outside should be zeros")
	}
	if got, _ := s.ReadRange(500, 500); len(got[0]) != 0 {
		t.Error("empty range")
	}
	s.release()
}

func TestSpoolLifetime(t *testing.T) {
	dir := t.TempDir()
	// Abort は書きかけのファイルを消す
	w, err := newSpoolWriter(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([][]float32{make([]float32, 100), make([]float32, 100)})
	path := w.path
	w.Abort()
	if fileExists(path) {
		t.Error("aborted spool file remains")
	}

	// 参照が0になるまで消えない。読み手を閉じるまで消えない(読み手も参照を持つ)
	s, _ := writeTestSpool(t, dir, 1000, 2)
	if !s.acquire() { // 使う側の参照
		t.Fatal("acquire failed")
	}
	r, err := s.open(0)
	if err != nil {
		t.Fatal(err)
	}
	s.release() // キャッシュの参照
	s.release() // 使う側の参照
	if !fileExists(s.path) {
		t.Fatal("removed while a reader is open")
	}
	r.Close() // 最後の参照
	if fileExists(s.path) {
		t.Error("spool remains after the last reference")
	}
	if s.acquire() {
		t.Error("acquire after release should fail")
	}
	if _, err := s.open(0); err == nil {
		t.Error("open after release should fail")
	}
}

func TestEngineCloseRemovesDir(t *testing.T) {
	e := newTestEngine(t)
	if err := e.Close(); err != nil { // 何も作っていなくても閉じられる
		t.Fatal(err)
	}
	dir, err := e.tempDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != filepath.Clean(os.TempDir()) {
		t.Errorf("dir %s not in temp", dir)
	}
	s, _ := writeTestSpool(t, dir, 500, 2)
	e.cache.putSpool("pa", "k", s)
	if got, ok := e.cache.lookupSpool("pa", "k"); !ok {
		t.Fatal("cache miss")
	} else {
		got.release()
	}
	if _, ok := e.cache.lookupSpool("pa", "other"); ok { // キーが違うと外れ、古いエントリは消える
		t.Fatal("unexpected hit")
	}
	if fileExists(s.path) {
		t.Error("stale spool should be deleted when its slot is replaced")
	}
	s2, _ := writeTestSpool(t, dir, 500, 2)
	e.cache.putSpool("direct", "k", s2)
	st := e.Stats()
	if st["pa"].Computed != 1 || st["pa"].Hits != 1 || st["direct"].Computed != 1 {
		t.Errorf("stats %+v", st)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if fileExists(dir) || fileExists(s2.path) {
		t.Error("temp dir remains after Close")
	}
	// Close の後でも、また使い始められる
	if _, err := e.tempDir(); err != nil {
		t.Fatal(err)
	}
	e.Close()
}

// CleanStaleTemp(OS の一時フォルダが対象)の本体は CleanStaleTempIn。実OSの一時フォルダを掃除しないよう、専用フォルダで確かめる。
func TestCleanStaleTemp(t *testing.T) {
	base := t.TempDir()
	old, _ := os.MkdirTemp(base, renderTempPrefix+"*")
	fresh, _ := os.MkdirTemp(base, previewTempPrefix+"*")
	other, _ := os.MkdirTemp(base, "unrelated-*")
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, past, past)
	os.Chtimes(other, past, past)
	CleanStaleTempIn(base, 24*time.Hour)
	if fileExists(old) {
		t.Error("stale render dir remains")
	}
	if !fileExists(fresh) || !fileExists(other) {
		t.Error("fresh or unrelated dir was removed")
	}
}
