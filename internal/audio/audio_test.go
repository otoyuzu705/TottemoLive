package audio

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeTone(t *testing.T, secs int) string {
	t.Helper()
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(t.TempDir(), "tone.wav")
	out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi",
		"-i", "sine=frequency=440:sample_rate=44100:duration="+string(rune('0'+secs)), path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

func TestDecode(t *testing.T) {
	path := makeTone(t, 4)
	all, err := Decode(context.Background(), path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(all[0]); n < 4*48000-100 || n > 4*48000+100 || len(all[1]) != len(all[0]) {
		t.Errorf("decoded length %d/%d", len(all[0]), len(all[1]))
	}
}

func TestPeaks(t *testing.T) {
	path := makeTone(t, 2)
	p, err := Peaks(context.Background(), path, 50)
	if err != nil || len(p) != 100 {
		t.Fatalf("peaks len=%d err=%v", len(p), err)
	}
	for i := 0; i < 50; i++ {
		if p[2*i] > -0.1 || p[2*i+1] < 0.1 {
			t.Fatalf("bucket %d: %v %v", i, p[2*i], p[2*i+1])
		}
	}
}

func TestWAV16(t *testing.T) {
	buf := [][]float32{{0, 0.5, -1, 2}, {0, -0.5, 1, -2}}
	b := WAV16(buf, 48000)
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || len(b) != 44+4*4 {
		t.Fatalf("header/len: %q %d", b[:12], len(b))
	}
	if binary.LittleEndian.Uint32(b[24:]) != 48000 || binary.LittleEndian.Uint16(b[22:]) != 2 {
		t.Error("fmt chunk")
	}
	s := func(i int) int16 { return int16(binary.LittleEndian.Uint16(b[44+2*i:])) }
	// 0.5 → 約 16384(ディザ ±1)、範囲外はクリップ
	if d := int(s(2)) - 16384; d < -2 || d > 2 {
		t.Errorf("0.5 -> %d", s(2))
	}
	if s(6) != 32767 || s(7) != -32768 {
		t.Errorf("clip: %d %d", s(6), s(7))
	}
}

// 同じファイルの ffprobe は1回だけ。デコードも、取り込みで調べた結果を再利用する。
// ファイルが差し替わった(サイズ・更新時刻が変わった)ら調べ直す。
func TestProbeIsCached(t *testing.T) {
	path := makeTone(t, 2)
	ctx := context.Background()
	before := probeRuns.Load()
	for i := 0; i < 3; i++ {
		if _, err := Probe(ctx, path); err != nil {
			t.Fatal(err)
		}
	}
	if n := probeRuns.Load() - before; n != 1 {
		t.Fatalf("ffprobe ran %d times for 3 probes of the same file", n)
	}
	if _, err := Decode(ctx, path, 48000, nil); err != nil { // デコードの長さ推定は Probe を使う
		t.Fatal(err)
	}
	if n := probeRuns.Load() - before; n != 1 {
		t.Errorf("decode re-ran ffprobe (%d runs in total)", n)
	}

	// 別の長さのファイルに差し替えると、調べ直して新しい長さになる
	other := makeTone(t, 3)
	data, err := os.ReadFile(other)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, path)
	if err != nil || info.DurationSec < 2.9 {
		t.Errorf("replaced file: %+v %v", info, err)
	}
	if n := probeRuns.Load() - before; n != 2 {
		t.Errorf("expected a re-probe after replacing the file, runs=%d", n)
	}

	// 失敗は保存しない(存在しないファイルは毎回エラー)
	missing := filepath.Join(t.TempDir(), "none.wav")
	if _, err := Probe(ctx, missing); err == nil {
		t.Error("missing file accepted")
	}
}
