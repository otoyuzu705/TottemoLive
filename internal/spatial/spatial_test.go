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
	set, _ := LoadSet("synthetic", 48000, SetOptions{HeadShadow: 1})
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
	// 基準距離(10 m)でゲイン1、近いと大きく(逆距離則)、遠いと小さく、上限は +12 dB(MaxGain)
	if Gain(10, 1) != 1 || math.Abs(Gain(5, 1)-2) > 1e-12 || math.Abs(Gain(20, 1)-0.5) > 1e-12 || Gain(40, 0) != 1 {
		t.Errorf("gain: %v %v %v %v", Gain(10, 1), Gain(5, 1), Gain(20, 1), Gain(40, 0))
	}
	if math.Abs(Gain(3, 1)-10.0/3) > 1e-12 || Gain(2.5, 1) != MaxGain || Gain(0.1, 1) != MaxGain || Gain(0, 1) != MaxGain {
		t.Errorf("gain should rise as 1/d and be capped at MaxGain: %v %v %v", Gain(3, 1), Gain(2.5, 1), Gain(0.1, 1))
	}
	if math.Abs(20*math.Log10(MaxGain)-12.04) > 0.01 {
		t.Errorf("MaxGain is %.2f dB, want +12 dB", 20*math.Log10(MaxGain))
	}
	if DelaySamples(34.3, 48000) != 4800 {
		t.Errorf("delay=%d", DelaySamples(34.3, 48000))
	}
}

func TestDirect(t *testing.T) {
	set, _ := LoadSet("synthetic", 48000, SetOptions{HeadShadow: 1})
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

func TestSub(t *testing.T) {
	in := make([]float32, 100)
	in[0] = 1
	first := func(x []float32) int {
		for i, v := range x {
			if v != 0 {
				return i
			}
		}
		return -1
	}
	// 遅延 = 距離の伝搬遅延 + 追加の遅延。減衰はサブ自身の距離(25 m, 逆距離則 → 0.4)
	out := Sub(in, 48000, 25, 300, 1)
	if want := DelaySamples(25, 48000) + 300; first(out) != want || math.Abs(float64(out[first(out)])-0.4) > 1e-6 {
		t.Errorf("index %d (want %d) value %v", first(out), want, out[first(out)])
	}
	if len(out) != len(in)+first(out) {
		t.Errorf("length %d", len(out))
	}
	// 追加の遅延なし(または負)は、伝搬遅延だけ
	if got := first(Sub(in, 48000, 40, 0, 1)); got != DelaySamples(40, 48000) {
		t.Errorf("no extra delay: index %d", got)
	}
	if got := first(Sub(in, 48000, 40, -5, 1)); got != DelaySamples(40, 48000) {
		t.Errorf("negative extra delay must be ignored: index %d", got)
	}
	// 基準距離(10 m)より近いと大きくなる(逆距離則)
	if v := Sub(in, 48000, 5, 0, 1); math.Abs(float64(v[first(v)])-2) > 1e-6 {
		t.Errorf("close sub gain %v, want 2", v[first(v)])
	}
}

// 基準点でサブとメインの音が同時に届くよう、サブのほうが近いぶんだけ追加の遅延を決める。遠いときは0。
// 基準点を固定するので、座席に依らない。
func TestSubAlignDelays(t *testing.T) {
	ref := [3]float64{0, 30, 1.2}
	mains := [][3]float64{{-12, 0, 8}, {12, 0, 8}}
	subs := [][3]float64{{-7, 1, 0.3}, {7, 1, 0.3}, {0, 90, 0.3}} // 3本目は基準点から見てメインより遠い(60 m先)
	got := SubAlignDelays(ref, mains, subs, 48000)
	// メインの平均距離と、サブの距離(基準点から)の差
	dist := func(p [3]float64) float64 {
		return math.Sqrt((p[0]-ref[0])*(p[0]-ref[0]) + (p[1]-ref[1])*(p[1]-ref[1]) + (p[2]-ref[2])*(p[2]-ref[2]))
	}
	meanMain := (dist(mains[0]) + dist(mains[1])) / 2
	for i := 0; i < 2; i++ {
		if want := DelaySamples(meanMain-dist(subs[i]), 48000); got[i] != want || want <= 0 {
			t.Errorf("sub %d: extra delay %d, want %d (>0)", i, got[i], want)
		}
	}
	if got[0] != got[1] {
		t.Errorf("symmetric subs at the reference point should get the same delay: %d vs %d", got[0], got[1])
	}
	if got[2] != 0 {
		t.Errorf("a sub farther than the mains must not be delayed: %d", got[2])
	}
	// 基準点でのサブの到着時刻 = メインの平均距離の到着時刻(同時に届く)
	if arrive := DelaySamples(dist(subs[0]), 48000) + got[0]; math.Abs(float64(arrive-DelaySamples(meanMain, 48000))) > 1 {
		t.Errorf("at the reference point the sub arrives at %d, mains at %d", arrive, DelaySamples(meanMain, 48000))
	}
}
