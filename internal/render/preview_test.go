package render

import (
	"context"
	"math"
	"testing"

	"livebin/internal/project"
)

func computed(e *Engine) map[string]int {
	out := map[string]int{}
	for k, v := range e.Stats() {
		out[k] = v.Computed
	}
	return out
}

// 変更したパラメーターが読まれる段から下流だけが再計算される。
func TestPreviewCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	base := testProject(makeSource(t, dir))
	base.Venue.Preset = "livehouse"
	base.Crowd.ClapRanges = []project.ClapRange{{Start: 0, End: 3}}

	cases := []struct {
		name   string
		mod    func(*project.Project)
		stages []string // 再計算されるべき段(それ以外は再利用)
	}{
		{"reverb.mix", func(p *project.Project) { p.Reverb.Mix = 0.6 }, nil},
		{"spatial.directLevelDb", func(p *project.Project) { p.Spatial.DirectLevelDb = -3 }, nil},
		{"crowd.levelDb", func(p *project.Project) { p.Crowd.LevelDb = 0 }, nil},
		{"output.targetLufs", func(p *project.Project) { p.Output.TargetLufs = -18 }, nil},
		{"pa.lowCutHz", func(p *project.Project) { p.PA.LowCutHz = 120 }, []string{"pa:0", "direct", "reverb"}},
		{"source gain", func(p *project.Project) { p.Sources[0].GainDb = -3 }, []string{"pa:0", "direct", "reverb"}},
		{"reverb.decayScale", func(p *project.Project) { p.Reverb.DecayScale = 0.6 }, []string{"reverb"}},
		{"reverb.preDelayMs", func(p *project.Project) { p.Reverb.PreDelayMs = 90 }, []string{"reverb"}},
		{"spatial.distanceRolloff", func(p *project.Project) { p.Spatial.DistanceRolloff = 0.5 }, []string{"direct"}},
		{"crowd.seed", func(p *project.Project) { p.Crowd.Seed = 9 }, []string{"crowd"}},
		{"crowd.keyframes", func(p *project.Project) { p.Crowd.Keyframes = nil }, []string{"crowd"}},
		{"listener", func(p *project.Project) { p.Listener.X = 3 }, []string{"direct", "crowd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine()
			if _, err := e.Preview(context.Background(), base, 0.5, 1.5, nil); err != nil {
				t.Fatal(err)
			}
			before := computed(e)
			q := base.Clone()
			tc.mod(&q)
			if _, err := e.Preview(context.Background(), q, 0.5, 1.5, nil); err != nil {
				t.Fatal(err)
			}
			after := computed(e)
			want := map[string]bool{}
			for _, s := range tc.stages {
				want[s] = true
			}
			for slot, n := range after {
				recomputed := n > before[slot]
				if recomputed != want[slot] {
					t.Errorf("slot %s: recomputed=%v want %v", slot, recomputed, want[slot])
				}
			}
		})
	}
}

// 同じ区間を同じ値で2回頼むと、全段がキャッシュから返る。
func TestPreviewCacheHitOnRepeat(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Venue.Preset = "livehouse"
	e := NewEngine()
	a, _ := e.Preview(context.Background(), p, 0, 1, nil)
	before := computed(e)
	b, _ := e.Preview(context.Background(), p, 0, 1, nil)
	for slot, n := range computed(e) {
		if n != before[slot] {
			t.Errorf("slot %s recomputed", slot)
		}
	}
	for i := range a.Audio[0] {
		if a.Audio[0][i] != b.Audio[0][i] {
			t.Fatal("cached result differs")
		}
	}
}

// 区間プレビューは、書き出しと同じ処理を区間に限っただけ(音量だけは区間で測るので別)。
func TestPreviewMatchesFullRender(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Venue.Preset = "livehouse"
	// マスターを実質素通しにして、波形そのものを比べる
	p.Output.TargetLufs = -30
	p.Output.CeilingDbTp = 0
	full, err := Render(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	const start, length = 1.2, 1.5
	prev, err := NewEngine().Preview(context.Background(), p, start, length, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := int(start * 48000)
	n := len(prev.Audio[0])
	if n != int(length*48000) {
		t.Fatalf("preview length %d", n)
	}
	// 最小二乗でゲインを合わせて、残差を見る
	var xy, xx float64
	for i := 0; i < n; i++ {
		xy += float64(prev.Audio[0][i]) * float64(full.Audio[0][s+i])
		xx += float64(prev.Audio[0][i]) * float64(prev.Audio[0][i])
	}
	g := xy / xx
	var res, sig float64
	for i := 0; i < n; i++ {
		d := float64(full.Audio[0][s+i]) - g*float64(prev.Audio[0][i])
		res += d * d
		sig += float64(full.Audio[0][s+i]) * float64(full.Audio[0][s+i])
	}
	if db := 10 * math.Log10(res/sig); db > -30 {
		t.Errorf("preview differs from full render: residual %.1f dB (gain %.3f)", db, g)
	}
}

func TestPreviewRangeEdges(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Venue.Preset = "livehouse"
	e := NewEngine()
	// 曲頭より前から・終端を越える区間でも長さが保たれる
	for _, c := range [][2]float64{{0, 1}, {2.5, 2}, {10, 1}} {
		r, err := e.Preview(context.Background(), p, c[0], c[1], nil)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if got := float64(len(r.Audio[0])) / 48000; math.Abs(got-c[1]) > 0.001 {
			t.Errorf("%v: length %.3f", c, got)
		}
	}
	if _, err := e.Preview(context.Background(), p, 0, 0, nil); err == nil {
		t.Error("zero length accepted")
	}
	if _, err := e.Preview(context.Background(), p, 0, MaxPreviewSec+1, nil); err == nil {
		t.Error("too long accepted")
	}
}

func TestOriginal(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Sources[0].GainDb = -6
	r, err := NewEngine().Original(context.Background(), p, 0.5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Audio[0]) != 48000 {
		t.Fatalf("length %d", len(r.Audio[0]))
	}
	// モノラル素材なので左右は同じ(バイノーラル化されていない)
	for i := range r.Audio[0] {
		if r.Audio[0][i] != r.Audio[1][i] {
			t.Fatal("original should be dual mono")
		}
	}
}
