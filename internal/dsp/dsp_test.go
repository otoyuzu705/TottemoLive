package dsp

import (
	"context"
	"fmt"
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

func direct(x, h []float32, o int) float64 {
	sum := 0.0
	for k := range h {
		if j := o - k; j >= 0 && j < len(x) {
			sum += float64(x[j]) * float64(h[k])
		}
	}
	return sum
}

func randSignal(rng *rand.Rand, n int) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = rng.Float32()*2 - 1
	}
	return x
}

func checkConv(t *testing.T, name string, got []float32, x, h []float32, step int) {
	t.Helper()
	if len(got) != len(x)+len(h)-1 {
		t.Fatalf("%s: len=%d want %d", name, len(got), len(x)+len(h)-1)
	}
	for o := 0; o < len(got); o += step {
		if want := direct(x, h, o); math.Abs(float64(got[o])-want) > 1e-3 {
			t.Fatalf("%s: o=%d got %v want %v", name, o, got[o], want)
		}
	}
}

// 公開のConvolveが、IR長ごとの分岐(短いIR・分割サイズ)をまたいで直接計算と一致する。
func TestConvolveMatchesDirect(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	cases := []struct{ nx, nh int }{
		{5000, 100}, {1, 1}, {7, 300}, {20000, shortIRMax}, {20000, shortIRMax + 1}, // 短い/境界
		{3000, 3500}, {1024, 1024}, {100, 5000}, {60000, 20000}, {30000, 140000}, // 長い(分割サイズが変わる)
	}
	for _, c := range cases {
		x, h := randSignal(rng, c.nx), randSignal(rng, c.nh)
		got, err := Convolve(context.Background(), x, h)
		if err != nil {
			t.Fatal(err)
		}
		checkConv(t, "Convolve", got, x, h, 97)
	}
}

// 分割サイズをどれにしても結果は同じ(分割サイズはIR長の倍数でなくてもよい)。
// 左右ペアの畳み込みは、別々に畳み込んだ結果と一致する(IR長が違っても、長い/短いが混ざっても)。
func TestConvolvePair(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	for _, c := range []struct{ nx, na, nb int }{
		{10000, 192, 192}, {10000, 100, 400}, {5, 192, 192}, {10000, 192, 3000}, {10000, 2000, 3000},
	} {
		x, a, b := randSignal(rng, c.nx), randSignal(rng, c.na), randSignal(rng, c.nb)
		ya, yb, err := ConvolvePair(context.Background(), x, a, b)
		if err != nil {
			t.Fatal(err)
		}
		checkConv(t, "pair A", ya, x, a, 53)
		checkConv(t, "pair B", yb, x, b, 53)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ConvolvePair(ctx, make([]float32, 200000), make([]float32, 100), make([]float32, 100)); err == nil {
		t.Error("expected cancellation error")
	}
}

func TestConvolvePartitionedAnyBlock(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	x, h := randSignal(rng, 40000), randSignal(rng, 9000)
	for _, B := range []int{1024, 2048, 4096, 8192, 16384} {
		got, err := convolvePartitioned(context.Background(), x, h, B)
		if err != nil {
			t.Fatal(err)
		}
		checkConv(t, fmt.Sprintf("B=%d", B), got, x, h, 101)
	}
}

func TestPartitionSize(t *testing.T) {
	for irLen, want := range map[int]int{513: BlockSize, 16000: BlockSize, 40000: 2560/1*0 + 4096, 149760: maxPartition, 1 << 22: maxPartition} {
		if got := partitionSize(irLen); got != want {
			t.Errorf("partitionSize(%d)=%d want %d", irLen, got, want)
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

// シェルフは持ち上げる側(+6 dB)でも、高域だけが上がり低域は変わらない。
func TestHighShelfBoost(t *testing.T) {
	const sr = 48000
	hi, lo := sine(12000, 0.2, sr, sr), sine(100, 0.2, sr, sr)
	HighShelf(sr, 4000, 6).Process(hi)
	HighShelf(sr, 4000, 6).Process(lo)
	rel := func(x []float32) float64 { return LinToDb(rms(x[sr/2:]) / (0.2 / math.Sqrt2)) }
	if got := rel(hi); math.Abs(got-6) > 0.5 {
		t.Errorf("high band raised by %.2f dB, want 6", got)
	}
	if got := rel(lo); math.Abs(got) > 0.2 {
		t.Errorf("low band moved by %.2f dB", got)
	}
}

// 低域シェルフは、上げる側・下げる側とも、低域だけが動き高域は変わらない。
func TestLowShelf(t *testing.T) {
	const sr = 48000
	for _, db := range []float64{-12, -6, 6, 9} {
		lo, hi := sine(30, 0.2, 2*sr, sr), sine(8000, 0.2, 2*sr, sr)
		LowShelf(sr, 200, db).Process(lo)
		LowShelf(sr, 200, db).Process(hi)
		rel := func(x []float32) float64 { return LinToDb(rms(x[sr:]) / (0.2 / math.Sqrt2)) }
		if got := rel(lo); math.Abs(got-db) > 0.5 {
			t.Errorf("%+v dB shelf: 30 Hz moved by %.2f dB", db, got)
		}
		if got := rel(hi); math.Abs(got) > 0.2 {
			t.Errorf("%+v dB shelf: 8 kHz moved by %.2f dB", db, got)
		}
	}
	// 0 dB は何もしない
	x := sine(100, 0.5, 2000, sr)
	y := append([]float32(nil), x...)
	LowShelf(sr, 200, 0).Process(y)
	for i := range x {
		if math.Abs(float64(x[i]-y[i])) > 1e-6 {
			t.Fatal("0 dB shelf changed the signal")
		}
	}
	// カットオフ付近では、ゲインのほぼ半分(シェルフの中点)
	mid := sine(200, 0.2, 2*sr, sr)
	LowShelf(sr, 200, 6).Process(mid)
	if got := LinToDb(rms(mid[sr:]) / (0.2 / math.Sqrt2)); math.Abs(got-3) > 0.6 {
		t.Errorf("midpoint gain %.2f dB, want ~3", got)
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

// 計算を省く判定(ガード)は、省いた場合と結果が完全に同じ。かつ静かな区間では実際に省ける。
func TestLimiterGuardKeepsResult(t *testing.T) {
	const sr = 48000
	rng := rand.New(rand.NewSource(3))
	// 静かな区間、ピークが上限付近の区間、サンプル間ピークが出る大振幅の区間を並べる
	x := make([]float32, 3*sr)
	copy(x, sine(300, 0.1, sr, sr))
	copy(x[sr:], sine(1000, 0.9, sr/2, sr))
	copy(x[sr+sr/2:], sine(11025, 1.4, sr/2, sr))
	for i := 2 * sr; i < 3*sr; i++ {
		x[i] = (rng.Float32()*2 - 1) * 0.3
	}
	buf := [][]float32{x, append([]float32(nil), x...)}
	ceil := DbToLin(-1)
	with, without := computeNeed(buf, ceil, true), computeNeed(buf, ceil, false)
	skippable := 0
	for i := range with {
		if with[i] != without[i] {
			t.Fatalf("guard changed need[%d]: %v vs %v", i, with[i], without[i])
		}
		if without[i] == 1 && localMax(buf, i-truePeakTaps/2, i+truePeakTaps/2)*interpGain <= ceil {
			skippable++
		}
	}
	if skippable < sr { // 静かな最初の1秒は省ける
		t.Errorf("guard rarely applies: %d samples", skippable)
	}
	if interpGain < 1 || interpGain > 3 {
		t.Errorf("interpGain=%v", interpGain)
	}
}

// クロスオーバー: 低域側・高域側それぞれが想定どおり減衰し、合計の振幅はフラットになる。
func TestLR4Crossover(t *testing.T) {
	const sr, fc = 48000, 90.0
	for _, f := range []float64{20, 45, 90, 180, 360, 1000, 5000} {
		x := sine(f, 1, 3*sr, sr)
		lo := append([]float32(nil), x...)
		hi := append([]float32(nil), x...)
		LR4LowPass(lo, sr, fc)
		LR4HighPass(hi, sr, fc)
		sum := make([]float32, len(x))
		for i := range sum {
			sum[i] = lo[i] + hi[i]
		}
		from := 2 * sr // 立ち上がりを除く
		gLo := rms(lo[from:]) / rms(x[from:])
		gHi := rms(hi[from:]) / rms(x[from:])
		gSum := rms(sum[from:]) / rms(x[from:])
		if math.Abs(gSum-1) > 0.02 {
			t.Errorf("%v Hz: sum gain %.3f (want 1)", f, gSum)
		}
		switch {
		case f == fc:
			// カットオフでは各側 -6 dB
			if math.Abs(LinToDb(gLo)+6.02) > 0.3 || math.Abs(LinToDb(gHi)+6.02) > 0.3 {
				t.Errorf("at fc: lo %.1f dB hi %.1f dB", LinToDb(gLo), LinToDb(gHi))
			}
		case f >= 4*fc && gLo > 0.01: // 2オクターブ上で -40 dB 以下(24 dB/oct)
			t.Errorf("%v Hz: low side leaks %.1f dB", f, LinToDb(gLo))
		case f <= fc/4 && gHi > 0.01:
			t.Errorf("%v Hz: high side leaks %.1f dB", f, LinToDb(gHi))
		}
	}
}
