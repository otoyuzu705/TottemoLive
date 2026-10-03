package render

import (
	"math"
	"testing"

	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
	"tottemolive/internal/venue"
)

// 残響は、最初に届く音(いちばん近いメインスピーカーの直接音)から始まる。
func TestFirstArrival(t *testing.T) {
	p := project.New()
	p.Venue.Speakers = []project.Speaker{{ID: "L", X: -12, Y: 0, Z: 8}, {ID: "R", X: 3, Y: 0, Z: 8}}
	p.Listener = project.Listener{X: 4, Y: 20, Z: 1.2}
	_, _, near := spatial.Direction(4, 20, 1.2, 0, 3, 0, 8) // 近いのは R
	if got, want := firstArrivalSamples(p), spatial.DelaySamples(near, sampleRate); got != want {
		t.Errorf("first arrival %d samples, want %d (nearest speaker)", got, want)
	}
	// 遠いほうだけにはならない
	_, _, far := spatial.Direction(4, 20, 1.2, 0, -12, 0, 8)
	if firstArrivalSamples(p) >= spatial.DelaySamples(far, sampleRate) {
		t.Error("must use the nearest speaker, not the farthest")
	}
	p.Venue.Speakers = nil
	if firstArrivalSamples(p) != 0 {
		t.Error("no speakers should give 0")
	}
}

// mix は残響を reverbDelay だけ遅らせて足し、直接音はそのまま足す。
func TestMixDelaysReverb(t *testing.T) {
	g := mixGains{direct: 1, reverb: 0.5}
	n := 400
	zero := func() [][]float32 { return [][]float32{make([]float32, n), make([]float32, n)} }
	direct, reverb := zero(), zero()
	direct[0][10] = 1
	reverb[0][0], reverb[1][5] = 1, 1
	const delay = 100
	out := mix(g, direct, reverb, delay)
	if len(out[0]) != n {
		t.Fatalf("length %d", len(out[0]))
	}
	if out[0][10] != 1 { // 直接音
		t.Errorf("direct %v", out[0][10])
	}
	if out[0][delay] != 0.5 || out[0][0] != 0 { // 残響は delay から始まる(× mix)
		t.Errorf("reverb: out[delay]=%v out[0]=%v", out[0][delay], out[0][0])
	}
	if out[1][5+delay] != 0.5 || out[1][5] != 0 {
		t.Errorf("reverb right: %v %v", out[1][5+delay], out[1][5])
	}
	// 出力の終わりを越える残響は切り捨てる(範囲外に書かない)
	reverb[0][n-1] = 1
	mix(g, direct, reverb, delay)
}

// 残響のゲインは会場の物理的な値: 臨界距離にいるとき、メイン全部の直接音と同じ大きさ(D/R = 0 dB)。
// 残響はリスナーの位置に依らず、直接音は距離で変わる。reverb.mix は基準値のとき物理値、0で無し、1で約+9 dB。
func TestMixGainsPhysicalReverb(t *testing.T) {
	arena, _ := venue.Get("arena")
	p := project.New() // アリーナ、メイン2本、mix は基準値
	g := mixGainsFor(p, arena)
	dc := venue.CriticalDistanceM(arena, p.Reverb.DecayScale)
	directAtDc := float64(len(p.Venue.Speakers)) * spatial.Gain(dc, p.Spatial.DistanceRolloff)
	if math.Abs(float64(g.reverb)-directAtDc) > 1e-6 {
		t.Errorf("reverb gain %.4f, want the direct gain at the critical distance %.4f", g.reverb, directAtDc)
	}
	// D/R は距離で変わる: 臨界距離の半分の席では直接音が +6 dB、倍の席では -6 dB
	dr := func(d float64) float64 {
		return 20 * math.Log10(float64(len(p.Venue.Speakers))*spatial.Gain(d, 1)/float64(g.reverb))
	}
	if math.Abs(dr(dc)) > 1e-6 || math.Abs(dr(dc/2)-6.02) > 0.05 || math.Abs(dr(dc*2)+6.02) > 0.05 {
		t.Errorf("D/R at dc/2, dc, 2dc: %.2f %.2f %.2f dB", dr(dc/2), dr(dc), dr(dc*2))
	}
	// reverb.mix: 0 で無し、基準値の倍で +6 dB
	p.Reverb.Mix = 0
	if mixGainsFor(p, arena).reverb != 0 {
		t.Error("mix 0 should silence the reverb")
	}
	p.Reverb.Mix = venue.NominalMix * 2
	if d := 20 * math.Log10(float64(mixGainsFor(p, arena).reverb)/float64(g.reverb)); math.Abs(d-6.02) > 0.01 {
		t.Errorf("double mix: %.2f dB", d)
	}
	// 残響を長くすると臨界距離が縮み、残響が大きくなる。直接音のゲインは directLevelDb だけで決まる
	p.Reverb.Mix = venue.NominalMix
	p.Reverb.DecayScale = 1.2
	if mixGainsFor(p, arena).reverb <= g.reverb {
		t.Error("longer decay should raise the physical reverb level")
	}
	p.Spatial.DirectLevelDb = -6
	if d := 20 * math.Log10(float64(mixGainsFor(p, arena).direct)); math.Abs(d+6) > 1e-6 {
		t.Errorf("direct gain %.2f dB, want -6", d)
	}
	// 会場が大きい(臨界距離が遠い)ほど、残響は直接音に対して小さい: 野外のほうがアリーナより小さい
	out, _ := venue.Get("outdoor")
	p = project.New()
	if mixGainsFor(p, out).reverb >= mixGainsFor(p, arena).reverb {
		t.Error("an open-air venue should have a quieter reverb than an arena")
	}
}

// サブの追加の遅延は、座席ではなく基準点(客席の中央)で決まる。座席を動かしても変わらず、
// 基準点から離れた席では、左右のサブの到着時刻が実際のように食い違う(以前は座席ごとに常に完全に揃っていた)。
func TestSubAlignIsFixedToTheReferencePoint(t *testing.T) {
	arena, _ := venue.Get("arena")
	p := project.New()
	base := subAlignDelays(p, arena)
	if len(base) != 2 || base[0] <= 0 || base[0] != base[1] {
		t.Fatalf("extra delays %v: symmetric subs should get the same positive delay", base)
	}
	for _, x := range []float64{-30, 0, 25} {
		q := p.Clone()
		q.Listener.X, q.Listener.Y = x, 50
		got := subAlignDelays(q, arena)
		if got[0] != base[0] || got[1] != base[1] {
			t.Errorf("listener x=%v moved the sub alignment: %v vs %v", x, got, base)
		}
	}
	// 基準点の位置: 客席の中央(x=0)、奥行きの半分
	if ref := subAlignReference(arena); ref[0] != 0 || ref[1] != arena.DepthM/2 {
		t.Errorf("reference %v", ref)
	}
	// 基準点にいるとき、左右のサブは同じ時刻に届き、横にずれた席では食い違う
	arrive := func(l project.Listener) [2]int {
		var a [2]int
		for i, s := range p.Venue.Subs {
			_, _, d := spatial.Direction(l.X, l.Y, l.Z, l.YawDeg, s.X, s.Y, s.Z)
			a[i] = spatial.DelaySamples(d, sampleRate) + base[i]
		}
		return a
	}
	center := arrive(project.Listener{X: 0, Y: arena.DepthM / 2, Z: 1.2})
	if center[0] != center[1] {
		t.Errorf("at the reference point the two subs should arrive together: %v", center)
	}
	side := arrive(project.Listener{X: 30, Y: arena.DepthM / 2, Z: 1.2})
	if side[0] == side[1] {
		t.Errorf("off-center the two subs should arrive at different times: %v", side)
	}
}
