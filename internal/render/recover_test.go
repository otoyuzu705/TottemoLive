package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"tottemolive/internal/audio"
)

// 使っている最中に Engine の作業ディレクトリが消されても、次のプレビューは作り直して成功する。
func TestEngineRecoversFromRemovedWorkDir(t *testing.T) {
	ctx := context.Background()
	p := synthTwoProject()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: t.TempDir()})
	first, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := e.tempDir()
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	// 全段がキャッシュに当たるはずだが、ファイルが消えているので外して、再計算して成功する
	again, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatalf("preview after the work dir was removed: %v", err)
	}
	sameAudio(t, "after removal", again, first)
	if n := spoolFiles(t, e); n != 3 {
		t.Errorf("%d spool files after recovery, want 3", n)
	}
	// 先行プレビューも同様
	if err := os.RemoveAll(func() string { d, _ := e.tempDir(); return d }()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.previewWindow(ctx, p, 0, 2); err != nil {
		t.Fatalf("window after the work dir was removed: %v", err)
	}
}

// キャッシュ当たりのスプールのファイルだけが消えても(フォルダは残る)、次のレンダリングは成功し、その段は作り直される。
func TestEngineRecoversFromRemovedSpoolFile(t *testing.T) {
	ctx := context.Background()
	p := synthTwoProject()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: t.TempDir()})
	first, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := e.tempDir()
	files, _ := filepath.Glob(filepath.Join(dir, "spool-*.f32"))
	if len(files) != 3 {
		t.Fatalf("%d spool files", len(files))
	}
	for _, f := range files {
		os.Remove(f)
	}
	again, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatalf("preview after the spool files were removed: %v", err)
	}
	sameAudio(t, "after spool removal", again, first)
	if st := e.Stats(); st["pa"].Computed != 2 || st["direct"].Computed != 2 || st["reverb"].Computed != 2 {
		t.Errorf("stages not recomputed: %+v", st)
	}

	// 1つだけ(直接音)消えた場合
	e.cache.mu.Lock()
	dsp := e.cache.slots["direct"].val.(*spool)
	e.cache.mu.Unlock()
	os.Remove(dsp.path)
	if _, err := e.Preview(ctx, p, nil); err != nil {
		t.Fatalf("preview after one spool file was removed: %v", err)
	}
	if st := e.Stats(); st["direct"].Computed != 3 || st["reverb"].Computed != 2 {
		t.Errorf("only the direct stage should be recomputed: %+v", st)
	}
}

// ファイルを開いた後(リーダーを持った状態)でなく、lookup の後・読む前に消された場合は、1回やり直して成功する。
func TestRenderRetriesWhenSpoolVanishesAfterLookup(t *testing.T) {
	ctx := context.Background()
	p := synthTwoProject()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: t.TempDir()})
	first, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 次のプレビューは全段が当たる。進捗の通知(ミックスが始まるとき)で、直接音のスプールを消す
	e.cache.mu.Lock()
	dsp := e.cache.slots["direct"].val.(*spool)
	e.cache.mu.Unlock()
	var once sync.Once
	sink := &memSink{}
	res, err := e.RenderTo(ctx, p, func(stage string, ratio float64) {
		once.Do(func() { os.Remove(dsp.path) })
	}, sink)
	if err != nil {
		t.Fatalf("render with a vanished spool: %v", err)
	}
	res.Audio = sink.Audio
	sameAudio(t, "retry", res, first)
	if st := e.Stats(); st["direct"].Computed != 2 {
		t.Errorf("the direct stage should have been recomputed by the retry: %+v", st)
	}
}

// 使っている最中に Store の作業ディレクトリが消されても、次の書き込みは作り直して成功する。
func TestStoreRecoversFromRemovedWorkDir(t *testing.T) {
	s := NewStoreIn(2, t.TempDir())
	defer s.Close()
	if p := s.Put([]byte("RIFFxxxx")); p == "" {
		t.Fatal("Put failed")
	}
	dir, _ := s.tempDir()
	os.RemoveAll(dir)
	if p := s.Put([]byte("RIFFyyyy")); p == "" {
		t.Fatal("Put failed after the work dir was removed")
	}
	os.RemoveAll(func() string { d, _ := s.tempDir(); return d }())
	w := s.NewWAV()
	if err := w.Start(100, 48000); err != nil {
		t.Fatalf("Start after the work dir was removed: %v", err)
	}
	w.Abort()
}

func exportTestEngine() *Engine { return &Engine{openSource: synthOpener, chunk: 4096} }

func skipWithoutFFmpeg(t *testing.T) {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// 書き出しが成功すると既存のファイルが置き換わり、一時ファイル(.partial)は残らない。
func TestExportReplacesExistingFile(t *testing.T) {
	skipWithoutFFmpeg(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.wav")
	os.WriteFile(out, []byte("old contents"), 0o644)
	if err := exportWith(context.Background(), exportTestEngine(), synthProject(2), out, nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil || len(b) < 1000 || string(b[:4]) != "RIFF" {
		t.Fatalf("output not replaced: %d bytes %v", len(b), err)
	}
	if names := dirNames(t, dir); len(names) != 1 || names[0] != "out.wav" {
		t.Errorf("files left in the output dir: %v", names)
	}
	// 既存が無い場合も
	out2 := filepath.Join(dir, "new.wav")
	if err := exportWith(context.Background(), exportTestEngine(), synthProject(2), out2, nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range dirNames(t, dir) {
		if strings.Contains(n, ".partial") {
			t.Errorf("partial file left: %s", n)
		}
	}
}

// 書き出しの途中で中断すると、既存のファイルは元のまま残り、一時ファイルも残らない。失敗したときも同じ。
func TestExportCancelKeepsExistingFile(t *testing.T) {
	skipWithoutFFmpeg(t)
	dir := t.TempDir()
	out := filepath.Join(dir, "out.wav")
	os.WriteFile(out, []byte("old contents"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	err := exportWith(ctx, exportTestEngine(), synthProject(3), out, func(stage string, ratio float64) {
		if stage == StageEncode && ratio > 0.3 {
			once.Do(cancel)
		}
	})
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if b, _ := os.ReadFile(out); string(b) != "old contents" {
		t.Errorf("the existing file was modified: %q", b)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("files left in the output dir: %v", names)
	}

	// 中断が早い段階(エンコードが始まる前)でも同じ
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := exportWith(ctx2, exportTestEngine(), synthProject(3), out, nil); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if b, _ := os.ReadFile(out); string(b) != "old contents" {
		t.Errorf("the existing file was modified: %q", b)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("files left in the output dir: %v", names)
	}

	// 失敗(空のプロジェクト)でも既存は残る
	bad := synthProject(3)
	bad.Sources = nil
	if err := exportWith(context.Background(), exportTestEngine(), bad, out, nil); err == nil {
		t.Fatal("export without sources succeeded")
	}
	if b, _ := os.ReadFile(out); string(b) != "old contents" {
		t.Errorf("the existing file was modified: %q", b)
	}
	if names := dirNames(t, dir); len(names) != 1 {
		t.Errorf("files left in the output dir: %v", names)
	}
}
