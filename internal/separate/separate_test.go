package separate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 偽のDemucsをビルドして LIVEBIN_DEMUCS に設定する。
func useStub(t *testing.T) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "demucs.exe")
	if out, err := exec.Command("go", "build", "-o", exe, "./testdata/stubdemucs").CombinedOutput(); err != nil {
		t.Fatalf("build stub: %v\n%s", err, out)
	}
	t.Setenv("LIVEBIN_DEMUCS", exe)
}

func writeSong(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "曲 name.wav")
	if err := os.WriteFile(p, []byte("fake audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSeparateAndCache(t *testing.T) {
	useStub(t)
	song, cache := writeSong(t), t.TempDir()
	var ratios []float64
	st, err := Separate(context.Background(), song, cache, func(r float64) { ratios = append(ratios, r) })
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{st.Vocals, st.Backing} {
		if b, err := os.ReadFile(p); err != nil || string(b) != "fake audio" {
			t.Errorf("stem %s: %v", p, err)
		}
	}
	if len(ratios) < 3 || ratios[0] != 0.1 || ratios[len(ratios)-1] != 1 {
		t.Errorf("progress %v", ratios)
	}
	for i := 1; i < len(ratios); i++ {
		if ratios[i] < ratios[i-1] {
			t.Errorf("progress went backwards: %v", ratios)
		}
	}

	// 2回目は再実行せずキャッシュから返す(Demucsが失敗する環境でも成功する)
	t.Setenv("STUB_FAIL", "1")
	again, err := Separate(context.Background(), song, cache, nil)
	if err != nil || again != st {
		t.Errorf("cache miss: %v %+v", err, again)
	}
	// 音源が変わったら再実行される(今は失敗する設定なのでエラーになる)
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(song, []byte("changed audio!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Separate(context.Background(), song, cache, nil); err == nil {
		t.Error("changed source should not hit the cache")
	}
}

func TestSeparateFailureReportsReason(t *testing.T) {
	useStub(t)
	t.Setenv("STUB_FAIL", "1")
	_, err := Separate(context.Background(), writeSong(t), t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Errorf("error should include the tool's message: %v", err)
	}
}

func TestSeparateCancel(t *testing.T) {
	useStub(t)
	t.Setenv("STUB_SLEEP", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := Separate(ctx, writeSong(t), t.TempDir(), nil)
	if err == nil || time.Since(start) > 10*time.Second {
		t.Errorf("expected prompt cancellation, got %v after %v", err, time.Since(start))
	}
}

func TestUnavailable(t *testing.T) {
	t.Setenv("LIVEBIN_DEMUCS", filepath.Join(t.TempDir(), "no-such-demucs"))
	if Available() {
		t.Error("should be unavailable")
	}
	if _, err := Separate(context.Background(), writeSong(t), t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "Demucs") {
		t.Errorf("error: %v", err)
	}
	if _, err := Separate(context.Background(), filepath.Join(t.TempDir(), "missing.wav"), t.TempDir(), nil); err == nil {
		t.Error("missing source accepted")
	}
}
