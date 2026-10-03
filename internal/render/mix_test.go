package render

import (
	"testing"

	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
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

// mix は残響を reverbDelay だけ遅らせて足し、直接音・客席はそのまま足す。
func TestMixDelaysReverb(t *testing.T) {
	p := project.New()
	p.Reverb.Mix = 0.5
	p.Spatial.DirectLevelDb = 0
	p.Crowd.LevelDb = 0
	n := 400
	zero := func() [][]float32 { return [][]float32{make([]float32, n), make([]float32, n)} }
	direct, reverb, crowd := zero(), zero(), zero()
	direct[0][10] = 1
	reverb[0][0], reverb[1][5] = 1, 1
	crowd[1][20] = 1
	const delay = 100
	out := mix(p, direct, reverb, crowd, delay)
	if len(out[0]) != n {
		t.Fatalf("length %d", len(out[0]))
	}
	if out[0][10] != 0.5 { // 直接音 × (1 - mix)
		t.Errorf("direct %v", out[0][10])
	}
	if out[0][delay] != 0.5 || out[0][0] != 0 { // 残響は delay から始まる(× mix)
		t.Errorf("reverb: out[delay]=%v out[0]=%v", out[0][delay], out[0][0])
	}
	if out[1][5+delay] != 0.5 || out[1][5] != 0 {
		t.Errorf("reverb right: %v %v", out[1][5+delay], out[1][5])
	}
	if out[1][20] != 1 {
		t.Errorf("crowd %v", out[1][20])
	}
	// 出力の終わりを越える残響は切り捨てる(範囲外に書かない)
	reverb[0][n-1] = 1
	mix(p, direct, reverb, crowd, delay)
}
