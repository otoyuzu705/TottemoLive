package spatial

import (
	"math"
	"math/cmplx"
	"testing"

	"tottemolive/internal/dsp"
	"tottemolive/internal/params"
)

// respDb はFIR h の周波数 f(Hz)での振幅(dB)。
func respDb(h []float32, f float64, sr int) float64 {
	var s complex128
	for n, v := range h {
		s += complex(float64(v), 0) * cmplx.Exp(complex(0, -2*math.Pi*f*float64(n)/float64(sr)))
	}
	return 20 * math.Log10(cmplx.Abs(s))
}

func mustSet(t *testing.T, name string, shadow float64) Set {
	t.Helper()
	set, err := LoadSet(name, 48000, SetOptions{HeadShadow: shadow})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s: %.2f, want %.2f ± %.2f", what, got, want, tol)
	}
}

func TestFirstOrderShelf(t *testing.T) {
	// 直流 0 dB、ナイキストで hfGain、中間は単調
	for _, g := range []float64{0.1, 0.5, 2} {
		b := dsp.FirstOrderShelf(48000, 1248, g)
		h := make([]float32, 4096)
		h[0] = 1
		b.Process(h)
		near(t, "dc", respDb(h, 0, 48000), 0, 1e-3)
		near(t, "nyquist", respDb(h, 24000, 48000), 20*math.Log10(g), 0.05)
		prev := 0.0
		for _, f := range []float64{100, 500, 1248, 3000, 8000, 16000, 23000} {
			d := respDb(h, f, 48000)
			if (d-prev)*(math.Log10(g)) < -1e-9 {
				t.Errorf("g=%v not monotonic at %v Hz", g, f)
			}
			prev = d
		}
	}
}

func TestBrownDudaShadow(t *testing.T) {
	set := mustSet(t, BrownDudaName, 1)
	h := set.Lookup(90, 0) // 真右
	near(t, "right 20 Hz", respDb(h.R, 20, 48000), 0, 0.1)
	near(t, "left 20 Hz", respDb(h.L, 20, 48000), 0, 0.1)
	near(t, "right 16k (ipsi)", respDb(h.R, 16000, 48000), 6.0, 0.5)
	near(t, "left 16k (theta=180)", respDb(h.L, 16000, 48000), -10.9, 1.0)
	near(t, "right ear at -60", respDb(set.Lookup(-60, 0).R, 16000, 48000), -19.1, 1.5)
	min, minAz := 0.0, 0
	for az := -90; az <= -30; az += 5 {
		if d := respDb(set.Lookup(float64(az), 0).R, 16000, 48000); d < min {
			min, minAz = d, az
		}
	}
	if minAz != -60 {
		t.Errorf("deepest right-ear shadow at %d deg, want -60", minAz)
	}
}

func TestBrownDudaStrength(t *testing.T) {
	flat := mustSet(t, BrownDudaName, 0)
	for _, az := range []float64{0, 45, 90, 135, 180, -90} {
		for _, el := range []float64{-40, 0, 40} {
			h := flat.Lookup(az, el)
			for _, f := range []float64{100, 1000, 4000, 16000} {
				near(t, "L flat", respDb(h.L, f, 48000), 0, 0.05)
				near(t, "R flat", respDb(h.R, f, 48000), 0, 0.05)
			}
		}
	}
	near(t, "s=0.5", respDb(mustSet(t, BrownDudaName, 0.5).Lookup(90, 0).R, 16000, 48000), 3.0, 0.3)
	near(t, "s=1.5", respDb(mustSet(t, BrownDudaName, 1.5).Lookup(90, 0).R, 16000, 48000), 9.0, 0.5)
}

func TestBrownDudaGeometry(t *testing.T) {
	set := mustSet(t, BrownDudaName, 1)
	h := set.Lookup(90, 0)
	itd := float64(peakIndex(h.L)-peakIndex(h.R)) / 48000
	if itd < 0.0004 || itd > 0.0008 {
		t.Errorf("ITD=%v", itd)
	}
	if peakIndex(h.R) >= peakIndex(h.L) {
		t.Errorf("right ear should lead: R=%d L=%d", peakIndex(h.R), peakIndex(h.L))
	}
	f := set.Lookup(0, 0)
	for i := range f.L {
		if math.Abs(float64(f.L[i]-f.R[i])) > 1e-6 {
			t.Fatal("front HRIR should be symmetric")
		}
	}
	if l := set.Lookup(-90, 0); math.Abs(energy(l.L)-energy(h.R)) > 1e-6 {
		t.Error("mirror mismatch")
	}
	if a, b := set.Lookup(190, 0), set.Lookup(-170, 0); energy(a.L) != energy(b.L) {
		t.Error("angle wrap mismatch")
	}
}

// 192タップで打ち切った尾が十分小さい。合成は強さ 1.5 で反対側の耳のカットオフが 1 kHz 未満まで下がり、
// 尾が最大 4e-5(−88 dB)になる(聞こえない大きさなので、そのぶんだけ許す)。
func TestHRIRTail(t *testing.T) {
	for _, name := range SetNames() {
		for _, s := range []float64{0, 0.5, 1, 1.5} {
			limit := 1e-8
			if name == SyntheticName && s > 1 {
				limit = 1e-4
			}
			set := mustSet(t, name, s)
			for _, az := range []float64{0, 45, 90, 135, 180, -90} {
				for _, el := range []float64{-40, 0, 40} {
					h := set.Lookup(az, el)
					for _, ear := range [][]float32{h.L, h.R} {
						for _, v := range ear[len(ear)-20:] {
							if math.Abs(float64(v)) >= limit {
								t.Fatalf("%s s=%v az=%v el=%v: tail %g", name, s, az, el, v)
							}
						}
					}
				}
			}
		}
	}
}

func TestHRIRQuantizedCache(t *testing.T) {
	for _, name := range SetNames() {
		set := mustSet(t, name, 1)
		a, b := set.Lookup(92, 3), set.Lookup(90, 0)
		if &a.L[0] != &b.L[0] {
			t.Errorf("%s: nearby directions should share the quantized HRIR", name)
		}
		// 強さの違う Set は互いに影響しない
		weak, strong := mustSet(t, name, 0.5), mustSet(t, name, 1)
		w1 := respDb(weak.Lookup(90, 0).R, 16000, 48000)
		strong.Lookup(90, 0)
		if w2 := respDb(weak.Lookup(90, 0).R, 16000, 48000); w1 != w2 {
			t.Errorf("%s: a set changed by another", name)
		}
		if respDb(strong.Lookup(90, 0).R, 16000, 48000) == w1 {
			t.Errorf("%s: sets with different strength should differ", name)
		}
	}
}

func TestHrirSetOptionsMatch(t *testing.T) {
	spec, ok := params.Find("spatial.hrirSet")
	if !ok {
		t.Fatal("spatial.hrirSet missing")
	}
	names := SetNames()
	if len(spec.Options) != len(names) {
		t.Fatalf("options %v vs names %v", spec.Options, names)
	}
	for i, n := range names {
		if spec.Options[i] != n {
			t.Errorf("option %d: %q vs %q", i, spec.Options[i], n)
		}
		if _, err := LoadSet(n, 48000, SetOptions{HeadShadow: 1}); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := LoadSet("nope", 48000, SetOptions{HeadShadow: 1}); err == nil {
		t.Error("unknown set accepted")
	}
}

func TestSyntheticHeadShadow(t *testing.T) {
	lr := func(s float64, f float64) float64 {
		h := mustSet(t, SyntheticName, s).Lookup(90, 0)
		return respDb(h.R, f, 48000) - respDb(h.L, f, 48000)
	}
	if !(lr(0.5, 4000) < lr(1, 4000)) {
		t.Errorf("weaker shadow should give a smaller L/R difference: %.1f vs %.1f", lr(0.5, 4000), lr(1, 4000))
	}
	for _, f := range []float64{200, 1000, 4000, 8000} {
		near(t, "s=0 ILD", lr(0, f), 0, 0.5)
	}
}

// refSyntheticBuild は、頭の影の強さを足す前の synthetic.build を一字一句写したもの
// (legacy_test.go は合成HRIRを新旧で共有するので、ここで既定(強さ1)のビット一致を守る)。
func refSyntheticBuild(sr int, azDeg, elDeg float64) HRIR {
	az, el := azDeg*math.Pi/180, elDeg*math.Pi/180
	sinLat := math.Sin(az) * math.Cos(el) // 右耳軸への射影(+1 が真右)
	lat := math.Asin(math.Max(-1, math.Min(1, sinLat)))
	itd := synHeadRadius / SpeedOfSound * (math.Abs(lat) + math.Abs(sinLat))
	front := 0.85 + 0.15*math.Cos(az) // 前 1.0、後ろ 0.7

	ear := func(sign float64) []float32 {
		s1 := sign * sinLat // 耳側に向くほど +1
		delay := float64(synBaseDelay)
		if s1 < 0 {
			delay += itd * float64(sr)
		}
		h := make([]float32, synTaps)
		for n := range h {
			t := float64(n) - delay
			v := 1.0
			if t != 0 {
				v = math.Sin(math.Pi*t) / (math.Pi * t)
			}
			w := 0.5 + 0.5*math.Cos(math.Pi*t/synBaseDelay)
			if math.Abs(t) > synBaseDelay {
				w = 0
			}
			h[n] = float32(v * w)
		}
		fc := synIpsiHz * math.Pow(synContraHz/synIpsiHz, (1-s1)/2) * front
		dsp.LowPass(float64(sr), fc).Process(h)
		g := float32(dsp.DbToLin(synIldDb * s1))
		for n := range h {
			h[n] *= g
		}
		return h
	}
	return HRIR{L: ear(-1), R: ear(+1)}
}

func TestSyntheticMatchesRef(t *testing.T) {
	set := mustSet(t, SyntheticName, 1)
	for az := -180.0; az < 180; az += synAzStepDeg { // +180 は Lookup が -180 に折り返す
		for el := -40.0; el <= 40; el += synElStepDeg {
			got, want := set.Lookup(az, el), refSyntheticBuild(48000, az, el)
			for i := range want.L {
				if got.L[i] != want.L[i] || got.R[i] != want.R[i] {
					t.Fatalf("az=%v el=%v tap %d differs", az, el, i)
				}
			}
		}
	}
}
