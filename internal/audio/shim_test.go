package audio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// shimEnv が設定されているとき、このテストバイナリは ffmpeg の shim として動く。
// Chocolatey や Scoop の ffmpeg.exe は、本物の ffmpeg を子プロセスとして起動して待つ小さな実行ファイルで、
// Kill で止まるのは shim だけになり、本物の ffmpeg はパイプを持ったまま残る(Windowsの利用者の環境で実際にある形)。
const shimEnv = "TOTTEMOLIVE_TEST_SHIM_REAL"

func TestMain(m *testing.M) {
	if real := os.Getenv(shimEnv); real != "" {
		cmd := exec.Command(real, os.Args[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// useShim は、以降の ffmpeg の起動を shim 経由にする。
func useShim(t *testing.T) {
	t.Helper()
	real, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg がないためスキップ")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(shimEnv, real)
	t.Setenv("TOTTEMOLIVE_FFMPEG", exe)
}

// within は f が d 以内に終わることを確かめる(固まったらテストを失敗させる)。
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s が %v 以内に終わらない(shim 経由の ffmpeg が残って、Wait が固まった)", what, d)
	}
}

// 読み切らずに Decoder を閉じても、shim 経由の ffmpeg(書き込みで詰まったまま残る)のせいで固まらない。
func TestDecoderCloseWithShim(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(t.TempDir(), "long.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=440:sample_rate=44100:duration=90", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	useShim(t)
	d, err := OpenDecoder(context.Background(), path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := [][]float32{make([]float32, 1000), make([]float32, 1000)}
	if _, err := d.Read(buf); err != nil { // 出力のパイプが満杯になるほど長い曲を、ほとんど読まずに閉じる
		t.Fatal(err)
	}
	within(t, 20*time.Second, "Decoder.Close", func() { d.Close() })
}

// ctx のキャンセルでも、shim 経由の ffmpeg が残って Read(Wait)が固まらない。
func TestDecoderCancelWithShim(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(t.TempDir(), "long.wav")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=440:sample_rate=44100:duration=90", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	useShim(t)
	ctx, cancel := context.WithCancel(context.Background())
	d, err := OpenDecoder(ctx, path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	buf := [][]float32{make([]float32, 1000), make([]float32, 1000)}
	if _, err := d.Read(buf); err != nil {
		t.Fatal(err)
	}
	cancel()
	within(t, 20*time.Second, "キャンセル後の Read", func() {
		for {
			if _, err := d.Read(buf); err != nil {
				return
			}
		}
	})
}

// 書き出しの中断(Abort)でも、shim 経由の ffmpeg のせいで固まらない。
func TestWAVEncoderAbortWithShim(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	useShim(t)
	e, err := NewWAVEncoder(context.Background(), filepath.Join(t.TempDir(), "out.wav"), 48000)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Write([][]float32{make([]float32, 48000), make([]float32, 48000)}); err != nil {
		t.Fatal(err)
	}
	within(t, 20*time.Second, "WAVEncoder.Abort", func() { e.Abort() })
}
