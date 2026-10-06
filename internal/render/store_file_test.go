package render

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func storeFiles(t *testing.T, s *Store) int {
	t.Helper()
	dir, err := s.tempDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func writePreview(t *testing.T, s *Store, frames int, bands []byte) (string, string) {
	t.Helper()
	w := s.NewWAV()
	if err := w.Start(frames, 48000); err != nil {
		t.Fatal(err)
	}
	buf := [][]float32{make([]float32, frames), make([]float32, frames)}
	for i := range buf[0] {
		buf[0][i] = 0.25
	}
	if err := w.Write(buf); err != nil {
		t.Fatal(err)
	}
	wav, bandsPath, err := s.Commit(w, bands)
	if err != nil {
		t.Fatal(err)
	}
	return wav, bandsPath
}

// PreviewWAV で書いた WAV は、Rangeつきで配信される。保持件数を超えたら古いファイルが消える。
func TestStoreServesPreviewWAVFromFile(t *testing.T) {
	s := NewStore(2)
	defer s.Close()
	wavPath, bandsPath := writePreview(t, s, 1000, []byte{1, 2, 3, 4})
	srv := httptest.NewServer(s)
	defer srv.Close()
	res, err := http.Get(srv.URL + wavPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || len(body) != 44+1000*4 || string(body[:4]) != "RIFF" || res.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("full: %d len=%d", res.StatusCode, len(body))
	}
	req, _ := http.NewRequest("GET", srv.URL+wavPath, nil)
	req.Header.Set("Range", "bytes=44-47")
	res, _ = http.DefaultClient.Do(req)
	part, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || !bytes.Equal(part, body[44:48]) {
		t.Errorf("range: %d %v", res.StatusCode, part)
	}
	res, _ = http.Get(srv.URL + bandsPath)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(b, []byte{1, 2, 3, 4}) {
		t.Errorf("bands: %d %v", res.StatusCode, b)
	}

	// 2件を超えたら古いものはファイルごと消える
	writePreview(t, s, 10, nil)
	writePreview(t, s, 10, nil)
	if n := storeFiles(t, s); n != 2 {
		t.Errorf("%d files remain, want 2", n)
	}
	res, _ = http.Get(srv.URL + wavPath)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("evicted: %d", res.StatusCode)
	}
}

// 配信中のファイルは、保持の対象から外れても、配信が終わるまで消えない。
func TestStoreKeepsFileWhileServing(t *testing.T) {
	s := NewStore(1)
	wavPath, _ := writePreview(t, s, 1000, nil)
	// 配信中(参照を1つ持っている)の状態で、次のプレビューが来て、古いほうが保持の対象から外れる
	s.mu.Lock()
	it := s.items["p1"]
	it.refs++
	s.mu.Unlock()
	writePreview(t, s, 10, nil)
	if n := storeFiles(t, s); n != 2 {
		t.Fatalf("%d files while serving, want 2 (the evicted one is still being served)", n)
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	if res, _ := http.Get(srv.URL + wavPath); res.StatusCode != 404 { // 新しい要求には配信しない
		t.Errorf("evicted item served to a new request: %d", res.StatusCode)
	}
	s.unref(it) // 配信が終わる
	if n := storeFiles(t, s); n != 1 {
		t.Errorf("%d files remain after serving ended, want 1", n)
	}
	dir, _ := s.tempDir()
	s.Close()
	if fileExists(dir) {
		t.Error("temp dir remains after Close")
	}
}

func TestPreviewWAVAbort(t *testing.T) {
	s := NewStore(2)
	defer s.Close()
	w := s.NewWAV()
	if err := w.Start(100, 48000); err != nil {
		t.Fatal(err)
	}
	w.Write([][]float32{make([]float32, 50), make([]float32, 50)})
	w.Abort()
	w.Abort()
	if n := storeFiles(t, s); n != 0 {
		t.Errorf("%d files after Abort", n)
	}
	// 宣言と違うフレーム数で閉じようとするとエラーで、ファイルも残らない
	w2 := s.NewWAV()
	w2.Start(100, 48000)
	w2.Write([][]float32{make([]float32, 50), make([]float32, 50)})
	if _, _, err := s.Commit(w2, nil); err == nil {
		t.Error("short WAV committed")
	}
	if n := storeFiles(t, s); n != 0 {
		t.Errorf("%d files after failed Commit", n)
	}
}
