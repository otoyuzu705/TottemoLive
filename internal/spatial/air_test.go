package spatial

import (
	"context"
	"math"
	"math/cmplx"
	"testing"
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
	set, _ := LoadSet("synthetic", sr)
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
