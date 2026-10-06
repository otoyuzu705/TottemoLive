package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefault(t *testing.T) {
	s := Default()
	if !s.CacheEnabled || s.CacheDir != "" || s.Version != Version {
		t.Errorf("default: %+v", s)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json") // 親フォルダが無くても保存できる
	want := Settings{CacheEnabled: false, CacheDir: filepath.Join(t.TempDir(), "c")}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	want.Version = Version
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("temp files left: %v", entries)
	}
}

func TestLoadMissingAndBroken(t *testing.T) {
	dir := t.TempDir()
	if got := Load(filepath.Join(dir, "none.json")); got != Default() {
		t.Errorf("missing: %+v", got)
	}
	for name, body := range map[string]string{"garbage": "{not json", "empty": "", "wrongtype": `{"cacheEnabled":"yes"}`} {
		p := filepath.Join(dir, name+".json")
		os.WriteFile(p, []byte(body), 0o644)
		if got := Load(p); got != Default() {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// 一部の項目だけのファイルは、無い項目が既定値になる
	p := filepath.Join(dir, "partial.json")
	os.WriteFile(p, []byte(`{"cacheDir":"/x"}`), 0o644)
	if got := Load(p); !got.CacheEnabled || got.CacheDir != "/x" {
		t.Errorf("partial: %+v", got)
	}
	// ディレクトリを指していても落ちない
	if got := Load(dir); got != Default() {
		t.Errorf("dir: %+v", got)
	}
}

func TestValidate(t *testing.T) {
	// 空は常に有効
	if s, err := Validate(Settings{CacheEnabled: true}); err != nil || s.CacheDir != "" {
		t.Errorf("empty: %+v %v", s, err)
	}
	// 相対パスは拒否
	if _, err := Validate(Settings{CacheDir: filepath.Join("relative", "dir")}); err == nil {
		t.Error("relative path accepted")
	}
	// 無ければ作成する
	base := t.TempDir()
	newDir := filepath.Join(base, "a", "b")
	s, err := Validate(Settings{CacheDir: newDir + string(filepath.Separator) + ".." + string(filepath.Separator) + "b"})
	if err != nil || s.CacheDir != newDir {
		t.Fatalf("create: %+v %v", s, err)
	}
	if fi, err := os.Stat(newDir); err != nil || !fi.IsDir() {
		t.Error("dir not created")
	}
	// 試し書きのファイルは残さない
	if entries, _ := os.ReadDir(newDir); len(entries) != 0 {
		t.Errorf("test file left: %v", entries)
	}
	// ファイルを指していたら拒否
	file := filepath.Join(base, "file")
	os.WriteFile(file, []byte("x"), 0o644)
	if _, err := Validate(Settings{CacheDir: file}); err == nil {
		t.Error("file accepted as dir")
	}
	// フォルダを作れないパス(ファイルの下)は拒否
	if _, err := Validate(Settings{CacheDir: filepath.Join(file, "sub")}); err == nil {
		t.Error("dir under a file accepted")
	}
}

func TestValidateNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows ではフォルダの属性で書き込みを止められない")
	}
	if os.Geteuid() == 0 {
		t.Skip("root は書き込める")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	os.Mkdir(dir, 0o555)
	if _, err := Validate(Settings{CacheDir: dir}); err == nil {
		t.Error("read-only dir accepted")
	}
}

func TestEffectiveDir(t *testing.T) {
	if (Settings{}).EffectiveDir() != os.TempDir() {
		t.Error("empty should be the OS temp dir")
	}
	if (Settings{CacheDir: "/x"}).EffectiveDir() != "/x" {
		t.Error("explicit dir")
	}
}

func TestFreeBytes(t *testing.T) {
	free, ok := FreeBytes(t.TempDir())
	if runtime.GOOS == "windows" || runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		if !ok || free <= 0 {
			t.Errorf("free space should be available on this OS: %d %v", free, ok)
		}
	}
	if _, ok := FreeBytes(filepath.Join(t.TempDir(), "does", "not", "exist")); ok {
		t.Error("nonexistent dir should fail")
	}
}
