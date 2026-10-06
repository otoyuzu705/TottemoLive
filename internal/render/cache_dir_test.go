package render

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startHookSink は Start(ミックスのスプールができた後、マスターを掛ける直前)で hook を呼ぶ捨て先。
type startHookSink struct {
	discardSink
	hook func()
}

func (s *startHookSink) Start(frames, sampleRate int) error {
	if s.hook != nil {
		s.hook()
	}
	return nil
}

// walkFiles は dir 以下の通常ファイルの一覧。
func walkFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func dirEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func synthEngineWith(t *testing.T, cfg EngineConfig) *Engine {
	t.Helper()
	e := NewEngineWith(cfg)
	e.openSource = synthOpener
	t.Cleanup(func() { e.Close() })
	return e
}

// 置き場所を指定すると、スプールはそのフォルダの下に作られ、Close で(置き場所のフォルダ自体は残して)消える。
func TestEngineSpoolsInConfiguredDir(t *testing.T) {
	base := t.TempDir()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: base})
	if _, err := e.Preview(context.Background(), synthTwoProject(), nil); err != nil {
		t.Fatal(err)
	}
	files := walkFiles(t, base)
	if len(files) != 3 {
		t.Fatalf("%d files under the cache dir, want 3 (pa, direct, reverb): %v", len(files), files)
	}
	for _, f := range files {
		if filepath.Dir(filepath.Dir(f)) != base || !strings.HasPrefix(filepath.Base(filepath.Dir(f)), renderTempPrefix) {
			t.Errorf("spool %s is not in a %s* dir directly under the cache dir", f, renderTempPrefix)
		}
	}
	if got := e.CacheBytes(); got <= 0 {
		t.Errorf("CacheBytes = %d", got)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if n := dirEntries(t, base); n != 0 {
		t.Errorf("%d entries remain in the cache dir after Close", n)
	}
}

// 置き場所のフォルダが後から消されても、次の処理で作り直す。
func TestEngineRecreatesMissingBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "gone", "cache")
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: base})
	if _, err := e.Preview(context.Background(), synthTwoProject(), nil); err != nil {
		t.Fatal(err)
	}
}

// キャッシュ無効: 段を保持せず、プレビューは毎回全段を計算し直す。使い捨てのスプールは作るが、終われば消える。
func TestDisabledCacheKeepsNothing(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: false, Dir: base})
	p := synthTwoProject()

	var first, second *Result
	var err error
	// 処理の途中(ミックスのスプールがある間)は、置き場所の下に使い捨てのスプールがある
	midFiles := 0
	sink := &startHookSink{hook: func() { midFiles = len(walkFiles(t, base)) }}
	if _, err = e.RenderTo(ctx, p, nil, sink); err != nil {
		t.Fatal(err)
	}
	if midFiles != 1 {
		t.Errorf("%d files while mixing, want 1 (the disposable mix spool)", midFiles)
	}
	if first, err = e.Preview(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if second, err = e.Preview(ctx, p, nil); err != nil { // 同じ値でも全部計算し直す
		t.Fatal(err)
	}
	sameAudio(t, "disabled cache", second, first)
	if first.PA == nil || first.PA.Series == nil {
		t.Error("PA spectrum should still be produced when the cache is off")
	}
	if st := e.Stats(); len(st) != 0 {
		t.Errorf("stats with the cache off: %+v", st)
	}
	if n := e.decodes.Load(); n < 4 {
		// 1回につき、レベル合わせの測定とバスで2回デコードする(測定値も保持しない)
		t.Errorf("decodes = %d, want every run to decode again", n)
	}
	if e.CacheBytes() != 0 {
		t.Error("CacheBytes should be 0")
	}
	if files := walkFiles(t, base); len(files) != 0 {
		t.Errorf("files left after the runs: %v", files)
	}
}

// キャッシュ無効でも、先行プレビューの使い捨てのPA段のスプールは置き場所の下に作られ、終われば消える。
func TestDisabledCacheWindowScratch(t *testing.T) {
	base := t.TempDir()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: false, Dir: base})
	if _, err := e.previewWindow(context.Background(), synthProject(8), 1, 2); err != nil {
		t.Fatal(err)
	}
	if files := walkFiles(t, base); len(files) != 0 {
		t.Errorf("files left after the window: %v", files)
	}
	if n := dirEntries(t, base); n != 0 {
		t.Errorf("%d dirs left after the window", n)
	}
}

// キャッシュなしのエンジン(書き出し)も、使い捨てのスプールを指定の置き場所に作って、終われば消す。
func TestRunScratchInConfiguredDir(t *testing.T) {
	base := t.TempDir()
	e := &Engine{baseDir: base, openSource: synthOpener}
	var mid []string
	sink := &startHookSink{hook: func() { mid = walkFiles(t, base) }}
	if _, err := e.RenderTo(context.Background(), synthProject(3), nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(mid) != 1 || !strings.HasPrefix(filepath.Base(filepath.Dir(mid[0])), renderTempPrefix) {
		t.Errorf("mix spool during the run: %v", mid)
	}
	if n := dirEntries(t, base); n != 0 {
		t.Errorf("%d entries left after the run", n)
	}
}

// ClearCache は保持している段を全部捨てて、ファイルも消す。次のプレビューはまた作る。
func TestClearCache(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	e := synthEngineWith(t, EngineConfig{CacheEnabled: true, Dir: base})
	p := synthTwoProject()
	if _, err := e.Preview(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	e.ClearCache()
	if e.CacheBytes() != 0 || len(walkFiles(t, base)) != 0 {
		t.Errorf("after ClearCache: %d bytes, files %v", e.CacheBytes(), walkFiles(t, base))
	}
	before := e.Stats()["pa"].Computed
	if _, err := e.Preview(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	if e.Stats()["pa"].Computed != before+1 || len(walkFiles(t, base)) != 3 {
		t.Error("the stages should be computed again after ClearCache")
	}
	// キャッシュ無効でも呼べる
	NewEngineWith(EngineConfig{}).ClearCache()
}

func TestStoreInConfiguredDir(t *testing.T) {
	base := t.TempDir()
	s := NewStoreIn(2, base)
	if p := s.Put([]byte("RIFFxxxx")); p == "" {
		t.Fatal("Put failed")
	}
	files := walkFiles(t, base)
	if len(files) != 1 || !strings.HasPrefix(filepath.Base(filepath.Dir(files[0])), previewTempPrefix) {
		t.Fatalf("store files: %v", files)
	}
	s.Close()
	if n := dirEntries(t, base); n != 0 {
		t.Errorf("%d entries left after Close", n)
	}
}

func TestCleanStaleTempIn(t *testing.T) {
	base := t.TempDir()
	old := filepath.Join(base, renderTempPrefix+"old")
	oldPreview := filepath.Join(base, previewTempPrefix+"old")
	fresh := filepath.Join(base, renderTempPrefix+"fresh")
	other := filepath.Join(base, "unrelated")
	for _, d := range []string{old, oldPreview, fresh, other} {
		os.Mkdir(d, 0o755)
	}
	past := time.Now().Add(-48 * time.Hour)
	for _, d := range []string{old, oldPreview, other} {
		os.Chtimes(d, past, past)
	}
	CleanStaleTempIn(base, 24*time.Hour)
	if fileExists(old) || fileExists(oldPreview) {
		t.Error("stale dirs remain")
	}
	if !fileExists(fresh) || !fileExists(other) {
		t.Error("fresh or unrelated dir was removed")
	}
	CleanStaleTempIn(filepath.Join(base, "missing"), time.Hour) // 無い場所でも落ちない
}
