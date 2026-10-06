package render

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJobsExclusiveKind(t *testing.T) {
	j := NewJobs()
	id1, ctx1, done1 := j.Begin(context.Background(), "preview")
	defer done1()
	_, ctx2, done2 := j.Begin(context.Background(), "preview")
	defer done2()
	select {
	case <-ctx1.Done():
	case <-time.After(time.Second):
		t.Fatal("old preview job was not cancelled")
	}
	if ctx2.Err() != nil {
		t.Error("new job should be alive")
	}
	if id1 == "" {
		t.Error("empty id")
	}
	// 種類が違えば干渉しない / kind なしは排他にならない
	_, ctx3, done3 := j.Begin(context.Background(), "")
	defer done3()
	_, ctx4, done4 := j.Begin(context.Background(), "")
	defer done4()
	if ctx3.Err() != nil || ctx4.Err() != nil || ctx2.Err() != nil {
		t.Error("unrelated jobs were cancelled")
	}
}

func TestJobsCancel(t *testing.T) {
	j := NewJobs()
	id, ctx, done := j.Begin(context.Background(), "")
	j.Cancel(id)
	if ctx.Err() == nil {
		t.Error("not cancelled")
	}
	done()
	j.Cancel(id)       // 終了済みIDは無視
	j.Cancel("nobody") // 存在しないIDも無視
}

func TestStoreServesWithRange(t *testing.T) {
	s := NewStore(2)
	defer s.Close()
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	path := s.Put(data)
	srv := httptest.NewServer(s)
	defer srv.Close()

	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || len(body) != 1000 || res.Header.Get("Content-Type") != "audio/wav" {
		t.Errorf("full: %d len=%d type=%q", res.StatusCode, len(body), res.Header.Get("Content-Type"))
	}
	if res.Header.Get("Accept-Ranges") != "bytes" {
		t.Error("Accept-Ranges missing")
	}

	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Range", "bytes=100-199")
	res, _ = http.DefaultClient.Do(req)
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || len(body) != 100 || body[0] != 100 {
		t.Errorf("range: %d len=%d", res.StatusCode, len(body))
	}

	for _, p := range []string{"/preview/nope.wav", "/other", "/preview/p1"} {
		res, _ = http.Get(srv.URL + p)
		res.Body.Close()
		if res.StatusCode != 404 {
			t.Errorf("%s: %d", p, res.StatusCode)
		}
	}

	// 保持件数を超えたら古いものから消える
	s.Put(data)
	s.Put(data)
	res, _ = http.Get(srv.URL + path)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Error("old preview should be evicted")
	}
}

func TestStoreServesBands(t *testing.T) {
	s := NewStore(2)
	defer s.Close()
	wavPath, bandsPath := s.PutWithBands([]byte("wavdata"), []byte{1, 2, 3, 4})
	if bandsPath == "" {
		t.Fatal("no bands path")
	}
	srv := httptest.NewServer(s)
	defer srv.Close()
	get := func(path string) (int, string, []byte) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), b
	}
	if code, typ, body := get(bandsPath); code != 200 || typ != "application/octet-stream" || len(body) != 4 || body[3] != 4 {
		t.Errorf("bands: %d %q %v", code, typ, body)
	}
	if code, typ, body := get(wavPath); code != 200 || typ != "audio/wav" || string(body) != "wavdata" {
		t.Errorf("wav: %d %q %q", code, typ, body)
	}
	// 帯域のバイナリが無いエントリは .bands が 404、WAVは配信できる
	onlyWav := s.Put([]byte("x"))
	if code, _, _ := get(strings.Replace(onlyWav, ".wav", ".bands", 1)); code != 404 {
		t.Errorf("missing bands should be 404, got %d", code)
	}
	if code, _, _ := get(onlyWav); code != 200 {
		t.Errorf("wav only entry: %d", code)
	}
	// 古いものから捨てる(帯域のバイナリも一緒に)
	s.Put([]byte("y"))
	if code, _, _ := get(bandsPath); code != 404 {
		t.Errorf("evicted bands should be 404, got %d", code)
	}
}
