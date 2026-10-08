package spatial

import (
	"context"
	"math"
	"math/cmplx"
	"testing"

	"gonum.org/v1/gonum/dsp/fourier"
)

// ISO 9613-1(20 ℃・相対湿度 50 %)の吸収係数が、公表されている典型値の範囲に収まる。
func TestAirAbsorptionMatchesISO(t *testing.T) {
	for _, c := range []struct{ f, lo, hi float64 }{ // dB/km
		{500, 1.5, 3.5}, {1000, 3.5, 6.5}, {2000, 7, 13}, {4000, 15, 32}, {8000, 55, 115}, {16000, 200, 480},
	} {
		got := AirAbsorptionDbPerM(c.f) * 1000
		if got < c.lo || got > c.hi {
			t.Errorf("%v Hz: %.1f dB/km, want within [%v, %v]", c.f, got, c.lo, c.hi)
		}
	}
	// 周波数とともに単調に増える
	prev := 0.0
	for f := 100.0; f <= 20000; f *= 1.3 {
		if a := AirAbsorptionDbPerM(f); a < prev {
			t.Errorf("absorption must grow with frequency: %v Hz", f)
		} else {
			prev = a
		}
	}
}

func firResponseDb(h []float32, f float64, sr int) float64 {
	var s complex128
	for n, v := range h {
		s += complex(float64(v), 0) * cmplx.Exp(complex(0, -2*math.Pi*f*float64(n)/float64(sr)))
	}
	return 20 * math.Log10(cmplx.Abs(s))
}

// 吸収フィルタの減衰が、物理値(scale × α(f) × 距離)に合う。直流ゲインは1、線形位相。
func TestAirFIRMatchesTarget(t *testing.T) {
	const sr = 48000
	if AirFIR(0, 1, sr) != nil || AirFIR(50, 0, sr) != nil {
		t.Error("no distance or no absorption should give nil")
	}
	for _, c := range []struct{ dist, scale float64 }{{25, 1}, {100, 1}, {100, 0.5}, {40, 2}} {
		h := AirFIR(c.dist, c.scale, sr)
		if len(h) != airTaps {
			t.Fatalf("taps %d", len(h))
		}
		if d := firResponseDb(h, 20, sr); math.Abs(d) > 0.2 {
			t.Errorf("dist %v: DC gain %.2f dB, want 0", c.dist, d)
		}
		for _, f := range []float64{1000, 4000, 8000, 12000} {
			want := -math.Min(c.scale*AirAbsorptionDbPerM(f)*c.dist, airMaxAttnDb)
			got := firResponseDb(h, f, sr)
			if math.Abs(got-want) > 1.0+0.05*math.Abs(want) {
				t.Errorf("dist %v scale %v: %v Hz attenuates %.2f dB, want %.2f", c.dist, c.scale, f, got, want)
			}
		}
		// 線形位相: インパルス応答が中心について対称
		for i := 0; i < airTaps/2; i++ {
			if math.Abs(float64(h[i]-h[airTaps-1-i])) > 1e-6 {
				t.Fatalf("not symmetric at %d", i)
			}
		}
	}
	// 距離が長いほど、高域がより減衰する(8 kHz)
	if firResponseDb(AirFIR(100, 1, sr), 8000, sr) >= firResponseDb(AirFIR(25, 1, sr), 8000, sr) {
		t.Error("longer distance should attenuate more")
	}
}

// Direct: 100 m 先では 8 kHz が物理値ぶん(約 -8 dB 以上)落ち、近いと落ちない。到着時刻は吸収の有無で変わらない。
func TestDirectAirAbsorption(t *testing.T) {
	const sr = 48000
	set, _ := LoadSet("synthetic", sr, SetOptions{HeadShadow: 1})
	in := make([]float32, 8192)
	for i := range in {
		in[i] = float32(math.Sin(2 * math.Pi * 8000 * float64(i) / sr))
	}
	level := func(dist, absorption float64) float64 {
		out, err := Direct(context.Background(), in, sr, dist, 0, 0, set, DirectParams{Rolloff: 0, AirAbsorption: absorption})
		if err != nil {
			t.Fatal(err)
		}
		d := DelaySamples(dist, sr)
		seg := out[0][d+2000 : d+6000]
		var s float64
		for _, v := range seg {
			s += float64(v) * float64(v)
		}
		return 10 * math.Log10(s/float64(len(seg)))
	}
	near, far := level(10, 1)-level(10, 0), level(100, 1)-level(100, 0)
	if near < -1.5 || near > 0.2 { // 10 m: 8 kHz の吸収は約 -0.8 dB
		t.Errorf("10 m: 8 kHz changed by %.2f dB", near)
	}
	if far > -5 || far < -14 { // 100 m: 約 -8 dB
		t.Errorf("100 m: 8 kHz changed by %.2f dB, want about -8", far)
	}
	// 到着時刻(インパルスのピーク)は、吸収の有無で数サンプルしか変わらない
	imp := make([]float32, 64)
	imp[0] = 1
	peak := func(absorption float64) int {
		out, _ := Direct(context.Background(), imp, sr, 34.3, 0, 0, set, DirectParams{AirAbsorption: absorption})
		return peakIndex(out[0])
	}
	if d := peak(1) - peak(0); d < -3 || d > 3 {
		t.Errorf("air absorption shifted the arrival by %d samples", d)
	}
}

// refAirFIR は、補正を足す前の AirFIR を一字一句写したもの
// (legacy_test.go は AirFIR を新旧で共有するので、ここで補正なしのビット一致を守る)。
func refAirFIR(dist, scale float64, sr int) []float32 {
	if dist <= 0 || scale <= 0 {
		return nil
	}
	// 周波数応答(振幅)を、0〜ナイキストの片側のbinに並べる。位相はゼロ(あとで中心にずらして線形位相にする)
	spec := make([]complex128, airGrid/2+1)
	for k := range spec {
		attn := math.Min(scale*AirAbsorptionDbPerM(float64(k)*float64(sr)/airGrid)*dist, airMaxAttnDb)
		spec[k] = complex(math.Pow(10, -attn/20), 0)
	}
	imp := fourier.NewFFT(airGrid).Sequence(nil, spec) // ゼロ位相のインパルス応答(循環)。正規化は airGrid で割る
	h := make([]float32, airTaps)
	c := AirGroupDelay
	var sum float64
	for i := range h {
		n := ((i-c)%airGrid + airGrid) % airGrid                   // 中心(0)の前後
		w := 0.5 + 0.5*math.Cos(math.Pi*float64(i-c)/float64(c+1)) // Hann窓
		v := imp[n] / airGrid * w
		h[i] = float32(v)
		sum += v
	}
	if sum > 0 { // 窓による直流ゲインのずれを戻す(低域の音量を変えない)
		for i := range h {
			h[i] = float32(float64(h[i]) / sum)
		}
	}
	return h
}

func TestAirFIRMatchesRef(t *testing.T) {
	const sr = 48000
	for _, c := range []struct{ dist, scale float64 }{{25, 1}, {100, 1}, {100, 0.5}, {40, 2}, {3, 1}} {
		want := refAirFIR(c.dist, c.scale, sr)
		for name, got := range map[string][]float32{
			"AirFIR":                 AirFIR(c.dist, c.scale, sr),
			"AirFIRComp empty":       AirFIRComp(c.dist, c.scale, AirComp{}, sr),
			"AirFIRComp amount zero": AirFIRComp(c.dist, c.scale, AirComp{Amount: 0, RefDistM: 30, MaxBoostDb: 12}, sr),
		} {
			if len(got) != len(want) {
				t.Fatalf("%s %+v: length %d vs %d", name, c, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%s %+v: tap %d differs", name, c, i)
				}
			}
		}
	}
}

// 補正EQの効き: 基準点では空気吸収がほぼ平坦になり、割合・上限・前の席の持ち上げが式どおり。
func TestAirFIRCompensation(t *testing.T) {
	const sr = 48000
	const d = 37.6
	alpha := AirAbsorptionDbPerM
	h := AirFIRComp(d, 1, AirComp{RefDistM: d, Amount: 1, MaxBoostDb: 18}, sr)
	for _, f := range []float64{100, 1000, 4000, 8000, 10000, 12500} {
		if got := firResponseDb(h, f, sr); math.Abs(got) > 0.3 {
			t.Errorf("at the reference point %v Hz: %.2f dB, want 0", f, got)
		}
	}
	h = AirFIRComp(d, 1, AirComp{RefDistM: d, Amount: 0.5, MaxBoostDb: 18}, sr)
	for _, f := range []float64{4000, 8000, 12500} {
		want := -0.5 * alpha(f) * d
		if got := firResponseDb(h, f, sr); math.Abs(got-want) > 0.3+0.05*math.Abs(want) {
			t.Errorf("amount 0.5 %v Hz: %.2f dB, want %.2f", f, got, want)
		}
	}
	h = AirFIRComp(d, 1, AirComp{RefDistM: d, Amount: 1, MaxBoostDb: 6}, sr)
	if got, want := firResponseDb(h, 16000, sr), -alpha(16000)*d+6; math.Abs(got-want) > 0.5 {
		t.Errorf("capped boost 16 kHz: %.2f dB, want %.2f", got, want)
	}
	// 基準点より前の席(距離 10 m)は、吸収が少ないぶん持ち上がる
	h = AirFIRComp(10, 1, AirComp{RefDistM: d, Amount: 1, MaxBoostDb: 18}, sr)
	if got, want := firResponseDb(h, 8000, sr), alpha(8000)*(d-10); math.Abs(got-want) > 0.3 {
		t.Errorf("front seat 8 kHz: %.2f dB, want %.2f", got, want)
	}
	// 補正だけのFIR
	c := AirComp{RefDistM: 30, Amount: 0.8, MaxBoostDb: 9}
	f := AirCompFIR(c, 1.5, sr)
	if len(f) != airTaps {
		t.Fatalf("taps %d", len(f))
	}
	for _, hz := range []float64{100, 4000, 8000, 12500, 16000} {
		want := math.Min(c.Amount*1.5*alpha(hz)*c.RefDistM, c.MaxBoostDb)
		if got := firResponseDb(f, hz, sr); math.Abs(got-want) > 0.3+0.05*want {
			t.Errorf("AirCompFIR %v Hz: %.2f dB, want %.2f", hz, got, want)
		}
	}
	if d := firResponseDb(f, 20, sr); math.Abs(d) > 0.05 {
		t.Errorf("DC %.3f dB", d)
	}
	for i := 0; i < airTaps/2; i++ {
		if math.Abs(float64(f[i]-f[airTaps-1-i])) > 1e-6 {
			t.Fatalf("not symmetric at %d", i)
		}
	}
	for name, got := range map[string][]float32{
		"amount 0": AirCompFIR(AirComp{RefDistM: 30, MaxBoostDb: 9}, 1, sr),
		"scale 0":  AirCompFIR(AirComp{RefDistM: 30, Amount: 1, MaxBoostDb: 9}, 0, sr),
		"ref 0":    AirCompFIR(AirComp{Amount: 1, MaxBoostDb: 9}, 1, sr),
	} {
		if got != nil {
			t.Errorf("%s: want nil", name)
		}
	}
}
