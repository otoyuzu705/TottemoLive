package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tottemolive/internal/settings"
)

func entryNames(t *testing.T, dir string) []string {
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

// countFilesWithPrefix は dir 以下で、親フォルダの名前が dirPrefix で始まるファイルの数。
func countFilesWithPrefix(dir, dirPrefix string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(filepath.Base(filepath.Dir(p)), dirPrefix) {
			n++
		}
		return nil
	})
	return n
}

func TestSettingsDefaultAndPersist(t *testing.T) {
	user := t.TempDir()
	a := newAppAt(user, t.TempDir())
	defer a.shutdown(nil)
	if got := a.GetSettings(); got != settings.Default() {
		t.Fatalf("default: %+v", got)
	}
	dir := filepath.Join(t.TempDir(), "cache")
	want := settings.Settings{CacheEnabled: false, CacheDir: dir}
	if err := a.SetSettings(want); err != nil {
		t.Fatal(err)
	}
	want.Version = settings.Version
	if got := a.GetSettings(); got != want {
		t.Errorf("got %+v", got)
	}
	// 次の起動で読み込まれる
	b := newAppAt(user, t.TempDir())
	defer b.shutdown(nil)
	if got := b.GetSettings(); got != want {
		t.Errorf("reloaded %+v", got)
	}
	if b.engine.CacheEnabled() {
		t.Error("the reloaded app should start with the cache off")
	}
}

// 壊れた設定ファイルでも、既定値で起動する。保存した置き場所が使えないときは、そのセッションだけ既定の場所を使う。
func TestSettingsBrokenAndUnusableDir(t *testing.T) {
	user := t.TempDir()
	os.MkdirAll(filepath.Join(user, appDir), 0o755)
	os.WriteFile(filepath.Join(user, appDir, "settings.json"), []byte("{broken"), 0o644)
	a := newAppAt(user, t.TempDir())
	if a.GetSettings() != settings.Default() {
		t.Errorf("broken file: %+v", a.GetSettings())
	}
	a.shutdown(nil)

	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o644)
	settings.Save(filepath.Join(user, appDir, "settings.json"), settings.Settings{CacheEnabled: true, CacheDir: filepath.Join(blocker, "sub")})
	b := newAppAt(user, t.TempDir())
	defer b.shutdown(nil)
	if got := b.GetSettings(); got.CacheDir != "" || !got.CacheEnabled {
		t.Errorf("unusable dir: %+v", got)
	}
}

// 検証に失敗したら、設定は変わらず、保存もされず、エンジンも差し替わらない。
func TestSetSettingsFailureChangesNothing(t *testing.T) {
	user := t.TempDir()
	a := newAppAt(user, t.TempDir())
	defer a.shutdown(nil)
	engine, store := a.engine, a.store
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, []byte("x"), 0o644)
	for name, s := range map[string]settings.Settings{
		"relative":   {CacheEnabled: false, CacheDir: filepath.Join("rel", "dir")},
		"under file": {CacheEnabled: false, CacheDir: filepath.Join(blocker, "sub")},
		"is a file":  {CacheEnabled: false, CacheDir: blocker},
	} {
		err := a.SetSettings(s)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if a.GetSettings() != settings.Default() {
			t.Errorf("%s: settings changed to %+v", name, a.GetSettings())
		}
		if a.engine != engine || a.store != store {
			t.Errorf("%s: engine/store were replaced", name)
		}
		if _, statErr := os.Stat(filepath.Join(user, appDir, "settings.json")); statErr == nil {
			t.Errorf("%s: a settings file was written", name)
		}
	}
}

// 置き場所を変えると、古い置き場所のスプールとフォルダが消え、新しい置き場所で始まる。
func TestSetSettingsMovesCacheDir(t *testing.T) {
	a, ev, p := newTestApp(t)
	dirA, dirB := t.TempDir(), t.TempDir()
	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dirA}); err != nil {
		t.Fatal(err)
	}
	r, err := a.RenderPreview(p)
	if err != nil || r.URL == "" {
		t.Fatalf("preview: %+v %v", r, err)
	}
	if n := countFilesWithPrefix(dirA, "tottemolive-render-"); n != 3 {
		t.Errorf("%d spool files in the cache dir, want 3", n)
	}
	if n := countFilesWithPrefix(dirA, "tottemolive-preview-"); n != 1 {
		t.Errorf("%d preview wav files in the cache dir, want 1", n)
	}
	if info := a.GetCacheInfo(); info.Dir != dirA || info.UsedBytes <= 0 || !info.Enabled {
		t.Errorf("cache info: %+v", info)
	}

	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dirB}); err != nil {
		t.Fatal(err)
	}
	ev.wait(t, "settings:applied")
	if left := entryNames(t, dirA); len(left) != 0 {
		t.Errorf("old cache dir still has %v", left)
	}
	if info := a.GetCacheInfo(); info.Dir != dirB || info.UsedBytes != 0 {
		t.Errorf("cache info after the move: %+v", info)
	}
	// 古いプレビューのURLは切れるが、エラーやクラッシュにはならず、新しいプレビューは新しい置き場所に作られる
	srv := httptest.NewServer(http.HandlerFunc(a.serveAssets))
	defer srv.Close()
	if res, err := http.Get(srv.URL + r.URL); err != nil || res.StatusCode != http.StatusNotFound {
		t.Errorf("old preview URL: %v %v", res, err)
	} else {
		res.Body.Close()
	}
	r2, err := a.RenderPreview(p)
	if err != nil || r2.URL == "" {
		t.Fatalf("preview after the move: %+v %v", r2, err)
	}
	if n := countFilesWithPrefix(dirB, "tottemolive-render-"); n != 3 {
		t.Errorf("%d spool files in the new cache dir, want 3", n)
	}
	if res, err := http.Get(srv.URL + r2.URL); err != nil || res.StatusCode != 200 {
		t.Errorf("new preview URL: %v %v", res, err)
	} else {
		res.Body.Close()
	}
}

// キャッシュを無効にすると既存のキャッシュは消え、プレビューは段を残さない。有効に戻すと、また保持する。
func TestSetSettingsTogglesCache(t *testing.T) {
	a, _, p := newTestApp(t)
	dir := t.TempDir()
	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RenderPreview(p); err != nil {
		t.Fatal(err)
	}
	if countFilesWithPrefix(dir, "tottemolive-render-") != 3 {
		t.Fatal("expected spools while the cache is on")
	}

	if err := a.SetSettings(settings.Settings{CacheEnabled: false, CacheDir: dir}); err != nil {
		t.Fatal(err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 {
		t.Errorf("%d spool files remain after disabling", n)
	}
	for i := 0; i < 2; i++ {
		if r, err := a.RenderPreview(p); err != nil || r.URL == "" {
			t.Fatalf("preview with the cache off: %+v %v", r, err)
		}
		if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 {
			t.Errorf("%d spool files left with the cache off", n)
		}
	}
	if st := a.engine.Stats(); len(st) != 0 {
		t.Errorf("stats with the cache off: %+v", st)
	}
	if info := a.GetCacheInfo(); info.UsedBytes != 0 || info.Enabled {
		t.Errorf("cache info: %+v", info)
	}
	// 先行プレビューも、使い捨てのPA段のスプールを残さない
	if w, err := a.RenderPreviewWindow(p, 0, false); err != nil || w.URL == "" {
		t.Fatalf("window: %+v %v", w, err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 {
		t.Errorf("%d spool files left after the window with the cache off", n)
	}

	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RenderPreview(p); err != nil {
		t.Fatal(err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 3 {
		t.Errorf("%d spool files after enabling, want 3", n)
	}
}

func TestClearCacheViaApp(t *testing.T) {
	a, _, p := newTestApp(t)
	dir := t.TempDir()
	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dir}); err != nil {
		t.Fatal(err)
	}
	r, err := a.RenderPreview(p)
	if err != nil {
		t.Fatal(err)
	}
	if a.GetCacheInfo().UsedBytes <= 0 {
		t.Fatal("no cache in use")
	}
	if err := a.ClearCache(); err != nil {
		t.Fatal(err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 || a.GetCacheInfo().UsedBytes != 0 {
		t.Errorf("%d spool files / %d bytes after ClearCache", n, a.GetCacheInfo().UsedBytes)
	}
	// 再生中のプレビューは消さない
	srv := httptest.NewServer(http.HandlerFunc(a.serveAssets))
	defer srv.Close()
	if res, err := http.Get(srv.URL + r.URL); err != nil || res.StatusCode != 200 {
		t.Errorf("preview after ClearCache: %v %v", res, err)
	} else {
		res.Body.Close()
	}
}

// 実行中のプレビューがあっても、設定を変えられる。プレビューは中断され(エラーにならず URL は空)、
// 次のプレビューは新しい設定で動く。
func TestSetSettingsDuringPreview(t *testing.T) {
	a, _, p := newTestApp(t)
	// プレビューがジョブとして走っている間(ジョブ登録後)に止めておき、設定の適用による中断で再開させる
	started := make(chan struct{})
	a.previewHook = func(ctx context.Context) {
		close(started)
		<-ctx.Done()
	}
	type result struct {
		r   PreviewResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, err := a.RenderPreview(p)
		done <- result{r, err}
	}()
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the preview did not start")
	}
	dir := t.TempDir()
	applied := make(chan error, 1)
	go func() { applied <- a.SetSettings(settings.Settings{CacheEnabled: false, CacheDir: dir}) }()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("SetSettings did not finish while a preview was running")
	}
	res := <-done
	if res.err != nil || res.r.URL != "" {
		t.Errorf("the interrupted preview should return an empty result and no error: %+v %v", res.r, res.err)
	}
	a.previewHook = nil
	if r, err := a.RenderPreview(p); err != nil || r.URL == "" {
		t.Fatalf("preview after the change: %+v %v", r, err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 {
		t.Errorf("%d spool files with the cache off", n)
	}
}

// blockingEmit は、最初の進捗イベントで止まる emit(書き出しを、任意の時点で止めておくためのテスト用の仕組み)。
// 止まったら blocked を閉じ、release が閉じられるまで返らない。
func blockingEmit(ev *events) (emit func(string, any), blocked, release chan struct{}) {
	blocked, release = make(chan struct{}), make(chan struct{})
	var once sync.Once
	emit = func(name string, data any) {
		if name == "render:progress" {
			once.Do(func() {
				close(blocked)
				<-release
			})
		}
		ev.emit(name, data)
	}
	return emit, blocked, release
}

// 書き出しは設定の変更で中断されず、使い捨てのスプールは開始時の置き場所に作られて、終われば消える。
// 書き出しを進捗の通知で止めておき、設定の変更(置き場所・キャッシュの切り替え)が終わった後に再開して、完走することを確かめる。
func TestExportUsesCacheDirAndSurvivesSettingsChange(t *testing.T) {
	a, ev, p := newTestApp(t)
	dir := t.TempDir()
	if err := a.SetSettings(settings.Settings{CacheEnabled: true, CacheDir: dir}); err != nil {
		t.Fatal(err)
	}
	emit, blocked, release := blockingEmit(ev)
	a.emit = emit
	out := filepath.Join(t.TempDir(), "out.wav")
	id, err := a.StartExport(p, out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(20 * time.Second):
		t.Fatal("the export did not start")
	}
	// 書き出しは進行中(止まっている)。この間に設定を変えても、待たされず、書き出しは中断されない
	if n := len(entryNames(t, dir)); n == 0 {
		t.Error("the running export has no scratch dir in the place it started with")
	}
	if err := a.SetSettings(settings.Settings{CacheEnabled: false, CacheDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	for _, e := range ev.snapshot() {
		if e.name == "render:error" {
			t.Fatalf("the export was interrupted by the settings change: %+v", e.data)
		}
	}
	close(release)
	done := ev.wait(t, "render:done").(DoneEvent)
	if done.JobID != id {
		t.Errorf("done: %+v", done)
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() < 1000 {
		t.Errorf("export output: %v %v", fi, err)
	}
	if n := countFilesWithPrefix(dir, "tottemolive-render-"); n != 0 {
		t.Errorf("%d scratch files left in the old dir", n)
	}
	if names := entryNames(t, filepath.Dir(out)); len(names) != 1 {
		t.Errorf("files in the output dir: %v", names)
	}
}

// 終了時(shutdown)は、実行中の書き出しを中断する。既存の出力ファイルは元のまま残り、一時ファイルも残らない。
func TestShutdownCancelsExportAndKeepsExistingFile(t *testing.T) {
	a, ev, p := newTestApp(t)
	emit, blocked, release := blockingEmit(ev)
	a.emit = emit
	outDir := t.TempDir()
	out := filepath.Join(outDir, "out.wav")
	os.WriteFile(out, []byte("old contents"), 0o644)
	if _, err := a.StartExport(p, out); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(20 * time.Second):
		t.Fatal("the export did not start")
	}
	finished := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(finished) }()
	time.Sleep(200 * time.Millisecond) // shutdown が最初にジョブを中断するのを待つ(書き出しは止まっているので、中断を見ない)
	close(release)
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	if e := ev.wait(t, "render:error").(ErrorEvent); e.Message != "中断しました" {
		t.Errorf("error event: %+v", e)
	}
	if b, _ := os.ReadFile(out); string(b) != "old contents" {
		t.Errorf("the existing output was modified: %q", b)
	}
	if names := entryNames(t, outDir); len(names) != 1 {
		t.Errorf("files left in the output dir: %v", names)
	}
}
