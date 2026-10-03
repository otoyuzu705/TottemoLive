package render

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
)

// 先行プレビューの窓は、曲全体の同じ範囲と(ラウドネスの推定の誤差を除いて)同じ音になる。
func TestPreviewWindowMatchesFull(t *testing.T) {
	dir := t.TempDir()
	p := testProject(makeSource(t, dir))
	full, err := NewEngine().Preview(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine()
	for _, start := range []float64{0, 1.5, 3.2, 5, 100} { // 100 は曲の外(終わり近くに寄せられる)
		w, err := e.previewWindow(context.Background(), p, start, 2)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(w.TotalSec-float64(len(full.Audio[0]))/48000) > 1e-9 {
			t.Errorf("start %v: total %.3f s, want %.3f s", start, w.TotalSec, float64(len(full.Audio[0]))/48000)
		}
		off := int(math.Round(w.StartSec * 48000))
		if len(w.Audio[0]) != 2*48000 || off+len(w.Audio[0]) > len(full.Audio[0]) {
			t.Fatalf("start %v: window %d samples at %d (full %d)", start, len(w.Audio[0]), off, len(full.Audio[0]))
		}
		// 窓と全体の同じ範囲を、最小二乗のゲインで合わせて、残差とゲインの差を見る
		var xy, xx, yy float64
		for c := 0; c < 2; c++ {
			for i, v := range w.Audio[c] {
				f := float64(full.Audio[c][off+i])
				xy += float64(v) * f
				xx += float64(v) * float64(v)
				yy += f * f
			}
		}
		if xx == 0 || yy == 0 {
			t.Fatalf("start %v: silent window", start)
		}
		gain := xy / xx // 窓に掛けると全体に合うゲイン
		resid := 1 - xy*xy/(xx*yy)
		gainDb := 20 * math.Log10(math.Abs(gain))
		t.Logf("start %.1f s (window at %.2f s): gain error %.2f dB, residual %.1e", start, w.StartSec, gainDb, resid)
		if resid > 5e-3 { // 形が一致する(−30 dB より小さい)
			t.Errorf("start %v: window differs from full (residual %.1e)", start, resid)
		}
		if math.Abs(gainDb) > 2 {
			t.Errorf("start %v: loudness estimate off by %.2f dB", start, gainDb)
		}
	}
	// 窓が曲全体なら、推定せず正確に一致する
	w, err := e.PreviewWindow(context.Background(), p, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Audio[0]) != len(full.Audio[0]) {
		t.Fatalf("whole-song window: %d vs %d samples", len(w.Audio[0]), len(full.Audio[0]))
	}
	for c := range w.Audio {
		for i := range w.Audio[c] {
			if d := math.Abs(float64(w.Audio[c][i] - full.Audio[c][i])); d > 1e-4 {
				t.Fatalf("whole-song window differs at ch%d sample %d by %v", c, i, d)
			}
		}
	}
}

// 窓の処理は、続く曲全体の処理のPA段を再利用できる(デコード・PAをやり直さない)。
func TestPreviewWindowSharesPAStage(t *testing.T) {
	dir := t.TempDir()
	p := testProject(makeSource(t, dir))
	e := NewEngine()
	if _, err := e.PreviewWindow(context.Background(), p, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Preview(context.Background(), p, nil); err != nil {
		t.Fatal(err)
	}
	st := e.Stats()
	if st["pa:0"].Computed != 1 || st["pa:0"].Hits < 1 {
		t.Errorf("pa stage: %+v", st["pa:0"])
	}
	if got := e.decodes.Load(); got != 1 {
		t.Errorf("decoded %d times, want 1", got)
	}
}

// 長い曲(音量が時間とともに変わる)でも、窓のラウドネスの推定が曲の位置に依らず数dB以内に収まる。
func TestPreviewWindowLoudnessOnLongSong(t *testing.T) {
	dir := t.TempDir()
	p := testProject(makeSource(t, dir)) // ffmpegの有無の確認
	long := filepath.Join(dir, "long.wav")
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=40:sample_rate=44100",
		"-f", "lavfi", "-i", "sine=frequency=1800:duration=40:sample_rate=44100",
		"-filter_complex", "[0]volume=0.5[a];[1]volume='0.05+0.4*abs(sin(t/6))':eval=frame[b];[a][b]amix=inputs=2:normalize=0", long).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	p.Sources[0].Path = long
	full, err := NewEngine().Preview(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine()
	for _, start := range []float64{0, 8, 18, 30} {
		w, err := e.previewWindow(context.Background(), p, start, 6)
		if err != nil {
			t.Fatal(err)
		}
		off := int(math.Round(w.StartSec * 48000))
		var xy, xx float64
		for c := 0; c < 2; c++ {
			for i, v := range w.Audio[c] {
				xy += float64(v) * float64(full.Audio[c][off+i])
				xx += float64(v) * float64(v)
			}
		}
		gainDb := 20 * math.Log10(math.Abs(xy/xx))
		t.Logf("start %.0f s: loudness estimate error %.2f dB", start, gainDb)
		if math.Abs(gainDb) > 1 {
			t.Errorf("start %v: loudness estimate off by %.2f dB", start, gainDb)
		}
	}
}
