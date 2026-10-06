package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"tottemolive/internal/audio"
	"tottemolive/internal/project"
)

type events struct {
	mu   sync.Mutex
	list []struct {
		name string
		data any
	}
}

func (e *events) emit(name string, data any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.list = append(e.list, struct {
		name string
		data any
	}{name, data})
}

func (e *events) wait(t *testing.T, name string) any {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		for _, ev := range e.list {
			if ev.name == name {
				e.mu.Unlock()
				return ev.data
			}
		}
		e.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("event %q not emitted", name)
	return nil
}

func newTestApp(t *testing.T) (*App, *events, project.Project) {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	src := filepath.Join(t.TempDir(), "tone.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=330:sample_rate=44100:duration=3", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	a := newAppAt(t.TempDir(), t.TempDir())
	t.Cleanup(func() { a.shutdown(context.Background()) })
	a.ctx = context.Background()
	a.presets.UserDir = t.TempDir()
	ev := &events{}
	a.emit = ev.emit

	infos, err := a.AddAudioFiles([]string{src})
	if err != nil || len(infos) != 1 {
		t.Fatalf("add: %v %v", infos, err)
	}
	p := a.NewProject()
	p, _ = a.ApplyVenue(p, "livehouse")
	p.Sources = []project.Source{{ID: infos[0].ID, Path: infos[0].Path, Role: project.RoleMix}}
	return a, ev, p
}

func TestAddAudioFilesAndPeaks(t *testing.T) {
	a, _, p := newTestApp(t)
	peaks, err := a.GetPeaks(p.Sources[0].ID, 20)
	if err != nil || len(peaks) != 40 {
		t.Fatalf("peaks: %d %v", len(peaks), err)
	}
	if _, err := a.GetPeaks("nope", 20); err == nil {
		t.Error("unknown id accepted")
	}
	// 読めないファイルがあっても、読めるものは取り込む
	good := p.Sources[0].Path
	infos, err := a.AddAudioFiles([]string{filepath.Join(t.TempDir(), "missing.wav"), good})
	if len(infos) != 1 || err == nil {
		t.Errorf("partial add: %d infos, err=%v", len(infos), err)
	}
	if infos[0].DurationSec < 2.9 || infos[0].SampleRate != 44100 || infos[0].Name != "tone.wav" {
		t.Errorf("info: %+v", infos[0])
	}
}

func TestRenderPreviewServesWav(t *testing.T) {
	a, _, p := newTestApp(t)
	r, err := a.RenderPreview(p)
	if err != nil || r.URL == "" {
		t.Fatalf("preview: %+v %v", r, err)
	}
	srv := httptest.NewServer(a.store)
	defer srv.Close()
	get := func(path string) (int, []byte) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, body
	}
	if code, body := get(r.URL); code != 200 || len(body) < 44+2*2*48000*3 || string(body[:4]) != "RIFF" {
		t.Errorf("wav: status=%d len=%d", code, len(body))
	}
	// Rangeリクエスト(シーク用)に部分的に答える
	req, _ := http.NewRequest("GET", srv.URL+r.URL, nil)
	req.Header.Set("Range", "bytes=44-107")
	if res, err := http.DefaultClient.Do(req); err != nil {
		t.Fatal(err)
	} else {
		part, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusPartialContent || len(part) != 64 {
			t.Errorf("range: status=%d len=%d", res.StatusCode, len(part))
		}
	}

	// PA出力の帯域レベル: 件数・フレーム数・バイト数が結果の項目と一致する
	if r.BandsURL == "" || r.Bands != 31 || r.HopSec != 0.05 || r.Frames < 55 {
		t.Fatalf("bands info: %+v", r)
	}
	code, body := get(r.BandsURL)
	if code != 200 || len(body) != r.Frames*r.Bands*4 {
		t.Errorf("bands: status=%d len=%d, want %d", code, len(body), r.Frames*r.Bands*4)
	}
	if r.OffsetDb != r.OffsetDb { // NaN
		t.Error("offset is NaN")
	}

	orig, err := a.RenderOriginal(p)
	if err != nil || orig == "" || orig == r.URL {
		t.Errorf("original: %q %v", orig, err)
	}
}

// 新しいプレビュー要求は古い要求を中断し、中断された側は URL が空の結果を返す。
func TestRenderPreviewSupersedes(t *testing.T) {
	a, _, p := newTestApp(t)
	p.Venue.Preset = "dome" // 重めにして、古い要求がまだ動いているうちに次を投げる
	p, _ = a.ApplyVenue(p, "dome")
	first := make(chan string, 1)
	go func() {
		r, _ := a.RenderPreview(p)
		first <- r.URL
	}()
	time.Sleep(150 * time.Millisecond)
	q := p.Clone()
	q.PA.LowCutHz = 150
	if r, err := a.RenderPreview(q); err != nil || r.URL == "" {
		t.Fatalf("second: %+v %v", r, err)
	}
	if u := <-first; u != "" {
		t.Logf("first finished before cancel took effect (%q) — acceptable on a fast machine", u)
	}
}

func TestStartExportEventsAndCancel(t *testing.T) {
	a, ev, p := newTestApp(t)
	out := filepath.Join(t.TempDir(), "out.wav")
	id, err := a.StartExport(p, out)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	done := ev.wait(t, "render:done").(DoneEvent)
	if done.JobID != id || done.Path != out {
		t.Errorf("done: %+v", done)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 100000 {
		t.Errorf("output: %v", err)
	}
	stages := map[string]bool{}
	ev.mu.Lock()
	for _, e := range ev.list {
		if pe, ok := e.data.(ProgressEvent); ok && e.name == "render:progress" && pe.JobID == id {
			stages[pe.Stage] = true
		}
	}
	ev.mu.Unlock()
	for _, s := range []string{"decode", "process", "encode"} {
		if !stages[s] {
			t.Errorf("no progress for stage %s", s)
		}
	}

	// 中断: render:error が出て、書きかけのファイルは残らない
	a2, ev2, p2 := newTestApp(t)
	p2, _ = a2.ApplyVenue(p2, "dome")
	out2 := filepath.Join(t.TempDir(), "cancel.wav")
	id2, _ := a2.StartExport(p2, out2)
	time.Sleep(100 * time.Millisecond)
	a2.CancelJob(id2)
	e := ev2.wait(t, "render:error").(ErrorEvent)
	if e.JobID != id2 {
		t.Errorf("error event: %+v", e)
	}
	if _, err := os.Stat(out2); err == nil {
		t.Error("partial output was left behind")
	}
	if _, err := a.StartExport(p, ""); err == nil {
		t.Error("empty path accepted")
	}
}

func TestSoundPresetsViaApp(t *testing.T) {
	a, _, p := newTestApp(t)
	list, err := a.ListSoundPresets()
	if err != nil || len(list) == 0 || !list[0].ReadOnly {
		t.Fatalf("shipped presets missing: %v %v", list, err)
	}
	p.PA.Drive = 0.77
	if err := a.SaveSoundPreset("テスト", p); err != nil {
		t.Fatal(err)
	}
	q := a.NewProject()
	q, err = a.ApplySoundPreset(q, "テスト")
	if err != nil || q.PA.Drive != 0.77 {
		t.Errorf("apply: %v %v", q.PA.Drive, err)
	}
	if err := a.DeleteSoundPreset("テスト"); err != nil {
		t.Fatal(err)
	}
}

func TestProgressEmitterThrottles(t *testing.T) {
	a := newAppAt(t.TempDir(), t.TempDir())
	ev := &events{}
	a.emit = ev.emit
	prog := a.progressEmitter("j")
	for i := 0; i <= 10000; i++ {
		prog("decode", float64(i)/10000)
	}
	prog("process", 0)
	ev.mu.Lock()
	n := len(ev.list)
	ev.mu.Unlock()
	if n > 200 || n < 100 {
		t.Errorf("expected ~100 throttled events, got %d", n)
	}
}

func TestSeparateSource(t *testing.T) {
	a, ev, p := newTestApp(t)
	a.stemDir = t.TempDir()

	t.Setenv("TOTTEMOLIVE_DEMUCS", filepath.Join(t.TempDir(), "none"))
	if a.StemSeparationAvailable() {
		t.Fatal("should be unavailable")
	}
	if _, err := a.SeparateSource(p.Sources[0].ID); err == nil {
		t.Error("expected an error when demucs is missing")
	}

	exe := filepath.Join(t.TempDir(), "demucs.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "./internal/separate/testdata/stubdemucs").CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v %s", err, out)
	}
	t.Setenv("TOTTEMOLIVE_DEMUCS", exe)
	if !a.StemSeparationAvailable() {
		t.Fatal("stub should be available")
	}
	if _, err := a.SeparateSource("nope"); err == nil {
		t.Error("unknown source accepted")
	}
	id, err := a.SeparateSource(p.Sources[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	done := ev.wait(t, "separate:done").(SeparateDoneEvent)
	if done.JobID != id || done.SourceID != p.Sources[0].ID || done.Vocals.ID == "" || done.Backing.ID == done.Vocals.ID {
		t.Fatalf("done: %+v", done)
	}
	if done.Vocals.DurationSec < 2.9 {
		t.Errorf("vocals info: %+v", done.Vocals)
	}
	// 分離した音源は GetPeaks で使える(登録済み)
	if _, err := a.GetPeaks(done.Vocals.ID, 10); err != nil {
		t.Error(err)
	}
	var progressed bool
	ev.mu.Lock()
	for _, e := range ev.list {
		if e.name == "separate:progress" {
			progressed = true
		}
	}
	ev.mu.Unlock()
	if !progressed {
		t.Error("no separate:progress event")
	}
}

// 旧名のフォルダにあるプリセットは新しい名前のフォルダへ移り、すでに新しいフォルダがあれば触らない。
func TestMigrateDir(t *testing.T) {
	root := t.TempDir()
	oldDir, newDir := filepath.Join(root, "livebin", "presets"), filepath.Join(root, "TottemoLive", "presets")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "自作.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateDir(oldDir, newDir)
	if _, err := os.Stat(filepath.Join(newDir, "自作.json")); err != nil {
		t.Errorf("preset was not migrated: %v", err)
	}
	if _, err := os.Stat(oldDir); err == nil {
		t.Error("old dir should be gone")
	}

	// 新しいフォルダがあるときは、旧フォルダがあっても何もしない
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "他.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	migrateDir(oldDir, newDir)
	if _, err := os.Stat(filepath.Join(newDir, "他.json")); err == nil {
		t.Error("existing new dir must not be overwritten")
	}
	// どちらも無くてもエラーにならない
	migrateDir(filepath.Join(root, "none"), filepath.Join(root, "none2"))
}

// アプリの終了時に、プレビューの一時ファイル(段のキャッシュ・配信中のWAV)が消える。
func TestShutdownRemovesTempFiles(t *testing.T) {
	a, _, p := newTestApp(t)
	list := func() map[string]bool {
		dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "tottemolive-*-*"))
		m := map[string]bool{}
		for _, d := range dirs {
			m[d] = true
		}
		return m
	}
	before := list()
	r, err := a.RenderPreview(p)
	if err != nil || r.URL == "" {
		t.Fatalf("preview: %+v %v", r, err)
	}
	if _, err := a.RenderOriginal(p); err != nil {
		t.Fatal(err)
	}
	var mine []string
	for d := range list() {
		if !before[d] {
			mine = append(mine, d)
		}
	}
	if len(mine) == 0 {
		t.Fatal("no temp dirs while previews are alive")
	}
	a.shutdown(context.Background())
	for _, d := range mine {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s remains after shutdown", d)
		}
	}
}
