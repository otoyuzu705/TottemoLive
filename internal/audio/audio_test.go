package audio

import (
	"context"
	"encoding/binary"
	"math"
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

func TestDecodeRange(t *testing.T) {
	path := makeTone(t, 4)
	ctx := context.Background()
	all, err := Decode(ctx, path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(all[0]); n < 4*48000-100 || n > 4*48000+100 {
		t.Errorf("full length %d", n)
	}
	seg, err := DecodeRange(ctx, path, 48000, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(seg[0]); n < 2*48000-100 || n > 2*48000+100 {
		t.Errorf("segment length %d", n)
	}
	// 区間は全体の該当部分と一致する(正弦波なので位相で確認)
	for i := 0; i < 1000; i++ {
		if d := math.Abs(float64(seg[0][i] - all[0][48000+i])); d > 0.02 {
			t.Fatalf("segment mismatch at %d: %v", i, d)
		}
	}
	// ファイルの終端を超える範囲は短く返る
	tail, err := DecodeRange(ctx, path, 48000, 3.5, 5)
	if err != nil || len(tail[0]) > 48000 {
		t.Errorf("tail len=%d err=%v", len(tail[0]), err)
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
