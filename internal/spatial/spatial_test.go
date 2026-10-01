package spatial

import (
	"context"
	"math"
	"testing"
)

func energy(x []float32) float64 {
	s := 0.0
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return s
}

func peakIndex(x []float32) int {
	best := 0
	for i, v := range x {
		if math.Abs(float64(v)) > math.Abs(float64(x[best])) {
			best = i
		}
	}
	return best
}

func TestDirection(t *testing.T) {
	// ステージ(0,0)方向を向く客席(0,25): 右スピーカーは右、左は左
	az, _, d := Direction(0, 25, 1.2, 0, 12, 0, 8)
	if az <= 0 || az > 90 || d < 25 {
		t.Errorf("right speaker az=%v d=%v", az, d)
	}
	az, _, _ = Direction(0, 25, 1.2, 0, -12, 0, 8)
	if az >= 0 {
		t.Errorf("left speaker az=%v", az)
	}
	// 真正面
	if az, _, _ := Direction(0, 25, 0, 0, 0, 0, 0); math.Abs(az) > 1e-9 {
		t.Errorf("front az=%v", az)
	}
	// 右回りに90°向くと、元の正面(ステージ)は左に来る
	if az, _, _ := Direction(0, 25, 0, 90, 0, 0, 0); math.Abs(az+90) > 1e-9 {
		t.Errorf("yaw90 az=%v", az)
	}
	// 背後
	if az, _, _ := Direction(0, 25, 0, 0, 0, 50, 0); math.Abs(math.Abs(az)-180) > 1e-9 {
		t.Errorf("back az=%v", az)
	}
}

func TestSyntheticHRIRLateralization(t *testing.T) {
	set, _ := LoadSet("synthetic", 48000)
	h := set.Lookup(90, 0) // 真右
	if energy(h.R) <= energy(h.L)*1.5 {
		t.Errorf("right ear should be louder: R=%v L=%v", energy(h.R), energy(h.L))
	}
	if peakIndex(h.R) >= peakIndex(h.L) {
		t.Errorf("right ear should lead: R=%d L=%d", peakIndex(h.R), peakIndex(h.L))
	}
	itd := float64(peakIndex(h.L)-peakIndex(h.R)) / 48000
	if itd < 0.0004 || itd > 0.0008 {
		t.Errorf("ITD=%v", itd)
	}
	// 正面は左右対称
	f := set.Lookup(0, 0)
	for i := range f.L {
		if math.Abs(float64(f.L[i]-f.R[i])) > 1e-6 {
			t.Fatal("front HRIR should be symmetric")
		}
	}
	// 左右反転
	l := set.Lookup(-90, 0)
	if math.Abs(energy(l.L)-energy(h.R)) > 1e-6 {
		t.Error("mirror mismatch")
	}
	// ±180を超える角度でも同じ結果
	if a, b := set.Lookup(190, 0), set.Lookup(-170, 0); energy(a.L) != energy(b.L) {
		t.Error("angle wrap mismatch")
	}
}

func TestDistanceModel(t *testing.T) {
	if Gain(5, 1) != 1 || math.Abs(Gain(20, 1)-0.5) > 1e-12 || Gain(40, 0) != 1 {
		t.Error("gain")
	}
	if Gain(0.1, 1) > MaxGain {
		t.Error("gain not capped")
	}
	if AirCutoffHz(50, 0) != 0 || AirCutoffHz(100, 1) >= AirCutoffHz(10, 1) || AirCutoffHz(1e6, 2) != AirMinHz {
		t.Error("air cutoff")
	}
	if DelaySamples(34.3, 48000) != 4800 {
		t.Errorf("delay=%d", DelaySamples(34.3, 48000))
	}
}

func TestDirect(t *testing.T) {
	set, _ := LoadSet("synthetic", 48000)
	in := make([]float32, 4800)
	in[0] = 1
	out, err := Direct(context.Background(), in, 48000, 34.3, 90, 0, set, DirectParams{Rolloff: 1})
	if err != nil {
		t.Fatal(err)
	}
	// 34.3m ≒ 100ms 遅れて右耳に先に届く
	r := peakIndex(out[1])
	if r < 4800 || r > 4800+80 {
		t.Errorf("arrival index %d", r)
	}
	if energy(out[1]) <= energy(out[0]) {
		t.Error("right should dominate")
	}
}
