package dsp

import (
	"context"
	"math"
	"math/rand"
	"testing"
)

func sine(freq, amp float64, n, sr int) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sr)))
	}
	return x
}

func rms(x []float32) float64 {
	s := 0.0
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(len(x)))
}

func TestConvolveMatchesDirect(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, c := range []struct{ nx, nh int }{{5000, 100}, {3000, 3500}, {1, 1}, {1024, 1024}, {100, 5000}} {
		x := make([]float32, c.nx)
		h := make([]float32, c.nh)
		for i := range x {
			x[i] = rng.Float32()*2 - 1
		}
		for i := range h {
			h[i] = rng.Float32()*2 - 1
		}
		got, err := Convolve(context.Background(), x, h)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != c.nx+c.nh-1 {
			t.Fatalf("len=%d want %d", len(got), c.nx+c.nh-1)
		}
		for o := 0; o < len(got); o += 7 {
			want := 0.0
			for k := 0; k < c.nh; k++ {
				if j := o - k; j >= 0 && j < c.nx {
					want += float64(x[j]) * float64(h[k])
				}
			}
			if math.Abs(float64(got[o])-want) > 1e-3 {
				t.Fatalf("nx=%d nh=%d o=%d got %v want %v", c.nx, c.nh, o, got[o], want)
			}
		}
	}
}

func TestConvolveCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Convolve(ctx, make([]float32, 200000), make([]float32, 100)); err == nil {
		t.Error("expected cancellation error")
	}
}

func TestHighPassAttenuatesLows(t *testing.T) {
	const sr = 48000
	low, high := sine(30, 1, sr, sr), sine(2000, 1, sr, sr)
	HighPass(sr, 200).Process(low)
	HighPass(sr, 200).Process(high)
	if rms(low[sr/2:]) > 0.05 || rms(high[sr/2:]) < 0.69 {
		t.Errorf("low=%v high=%v", rms(low[sr/2:]), rms(high[sr/2:]))
	}
}

func TestHighShelfGain(t *testing.T) {
	const sr = 48000
	hi, lo := sine(12000, 1, sr, sr), sine(100, 1, sr, sr)
	HighShelf(sr, 4000, -6).Process(hi)
	HighShelf(sr, 4000, -6).Process(lo)
	got := LinToDb(rms(hi[sr/2:]) / (1 / math.Sqrt2))
	if math.Abs(got+6) > 0.5 || math.Abs(LinToDb(rms(lo[sr/2:])/(1/math.Sqrt2))) > 0.2 {
		t.Errorf("shelf high=%v dB", got)
	}
}

func TestCompressReducesLoudPart(t *testing.T) {
	const sr = 48000
	x := sine(1000, 0.9, sr, sr)
	buf := [][]float32{x, append([]float32(nil), x...)}
	Compress(buf, sr, CompParams{ThresholdDb: -20, Ratio: 4, AttackMs: 1, ReleaseMs: 50})
	before := LinToDb(0.9)
	after := LinToDb(float64(maxAbs(buf[0][sr/2:])))
	// 定常状態: 超過量(約19dB)の3/4が削られる
	if before-after < 10 {
		t.Errorf("reduction too small: %v dB", before-after)
	}
	// Ratio=1 は何もしない
	y := sine(1000, 0.9, 1000, sr)
	z := append([]float32(nil), y...)
	Compress([][]float32{z}, sr, CompParams{ThresholdDb: -20, Ratio: 1, AttackMs: 1, ReleaseMs: 50})
	for i := range y {
		if y[i] != z[i] {
			t.Fatal("ratio 1 changed signal")
		}
	}
}

func maxAbs(x []float32) float32 {
	m := float32(0)
	for _, v := range x {
		m = max(m, float32(math.Abs(float64(v))))
	}
	return m
}

func TestSaturate(t *testing.T) {
	x := []float32{0.01, 0.5, 1}
	y := append([]float32(nil), x...)
	Saturate([][]float32{y}, 0)
	for i := range x {
		if x[i] != y[i] {
			t.Fatal("drive 0 changed signal")
		}
	}
	Saturate([][]float32{y}, 1)
	if math.Abs(float64(y[0]-0.01)) > 1e-3 || y[2] >= 0.5 || y[1] >= x[1] {
		t.Errorf("saturate: %v", y)
	}
}

func TestLUFSOfSine(t *testing.T) {
	const sr = 48000
	// 1kHzのフルスケール正弦波(片ch)は約 -3.01 LUFS
	x := sine(1000, 1, 10*sr, sr)
	got := IntegratedLUFS([][]float32{x, make([]float32, len(x))}, sr)
	if math.Abs(got-(-3.01)) > 0.1 {
		t.Errorf("LUFS=%v", got)
	}
	// 両chなら +3 dB
	got2 := IntegratedLUFS([][]float32{x, x}, sr)
	if math.Abs(got2-got-3.01) > 0.05 {
		t.Errorf("stereo LUFS=%v (mono %v)", got2, got)
	}
	if !math.IsInf(IntegratedLUFS([][]float32{make([]float32, sr)}, sr), -1) {
		t.Error("silence should be -Inf")
	}
	// 短い信号でもNaNにならない
	if v := IntegratedLUFS([][]float32{sine(1000, 0.5, 2000, sr)}, sr); math.IsNaN(v) {
		t.Error("NaN for short signal")
	}
}

func TestTruePeakLimit(t *testing.T) {
	const sr = 48000
	// サンプル間ピークが出る周波数・位相の大振幅信号
	x := sine(11025, 2.0, sr, sr)
	buf := [][]float32{x, sine(440, 1.5, sr, sr)}
	TruePeakLimit(buf, sr, -1)
	ceil := DbToLin(-1)
	for c, ch := range buf {
		// 別方式(8倍の理想sinc補間)で確認
		for n := 100; n < len(ch)-100; n += 3 {
			for f := 0.0; f < 1; f += 0.125 {
				sum := 0.0
				for k := -64; k <= 64; k++ {
					tt := float64(k) - f
					s := 1.0
					if tt != 0 {
						s = math.Sin(math.Pi*tt) / (math.Pi * tt)
					}
					sum += float64(ch[n+k]) * s * (0.5 + 0.5*math.Cos(math.Pi*tt/65))
				}
				if math.Abs(sum) > ceil*1.03 { // +0.25dB 以内
					t.Fatalf("ch%d n=%d f=%v peak %v > ceil %v", c, n, f, math.Abs(sum), ceil)
				}
			}
		}
	}
	// 小さい信号は変えない
	y := sine(440, 0.1, 5000, sr)
	z := append([]float32(nil), y...)
	TruePeakLimit([][]float32{z}, sr, -1)
	for i := range y {
		if math.Abs(float64(y[i]-z[i])) > 1e-6 {
			t.Fatal("limiter altered a quiet signal")
		}
	}
}
