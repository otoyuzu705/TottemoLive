package render

import (
	"context"
	"testing"

	"tottemolive/internal/project"
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
		{"pa.lowShelfDb", func(p *project.Project) { p.PA.LowShelfDb = 6 }, []string{"pa:0", "paSpectrum", "direct", "reverb"}},
		{"pa.lowShelfHz", func(p *project.Project) { p.PA.LowShelfHz = 200 }, []string{"pa:0", "paSpectrum", "direct", "reverb"}},
		{"pa.lowCutHz", func(p *project.Project) { p.PA.LowCutHz = 120 }, []string{"pa:0", "paSpectrum", "direct", "reverb"}},
		{"source gain", func(p *project.Project) { p.Sources[0].GainDb = -3 }, []string{"pa:0", "paSpectrum", "direct", "reverb"}},
		{"reverb.decayScale", func(p *project.Project) { p.Reverb.DecayScale = 0.6 }, []string{"reverb"}},
		{"reverb.lowCoherence", func(p *project.Project) { p.Reverb.LowCoherence = 0.2 }, []string{"reverb"}},
		{"reverb.lowDecayScale", func(p *project.Project) { p.Reverb.LowDecayScale = 2 }, []string{"reverb"}},
		{"reverb.lowLevelDb", func(p *project.Project) { p.Reverb.LowLevelDb = 9 }, []string{"reverb"}},
		{"reverb.lowCrossoverHz", func(p *project.Project) { p.Reverb.LowCrossoverHz = 400 }, []string{"reverb"}},
		{"reverb.preDelayMs", func(p *project.Project) { p.Reverb.PreDelayMs = 90 }, []string{"reverb"}},
		{"sub.levelDb", func(p *project.Project) { p.Sub.LevelDb = 9 }, []string{"direct"}},
		{"sub.crossoverHz", func(p *project.Project) { p.Sub.CrossoverHz = 120 }, []string{"direct"}},
		{"sub.enabled", func(p *project.Project) { p.Sub.Enabled = "off" }, []string{"direct"}},
		{"venue.subs", func(p *project.Project) { p.Venue.Subs[0].X = -3 }, []string{"direct"}},
		{"spatial.distanceRolloff", func(p *project.Project) { p.Spatial.DistanceRolloff = 0.5 }, []string{"direct"}},
		{"crowd.seed", func(p *project.Project) { p.Crowd.Seed = 9 }, []string{"crowd"}},
		{"crowd.keyframes", func(p *project.Project) { p.Crowd.Keyframes = nil }, []string{"crowd"}},
		{"listener", func(p *project.Project) { p.Listener.X = 3 }, []string{"direct", "crowd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEngine()
			if _, err := e.Preview(context.Background(), base, nil); err != nil {
				t.Fatal(err)
			}
			before := computed(e)
			q := base.Clone()
			tc.mod(&q)
			if _, err := e.Preview(context.Background(), q, nil); err != nil {
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
	a, _ := e.Preview(context.Background(), p, nil)
	before := computed(e)
	b, _ := e.Preview(context.Background(), p, nil)
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

// プレビューは書き出しと同じ処理なので、同じ音(音量も同じ)になる。
func TestPreviewMatchesRender(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Venue.Preset = "livehouse"
	full, err := Render(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := NewEngine().Preview(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prev.Audio[0]) != len(full.Audio[0]) {
		t.Fatalf("length %d vs %d", len(prev.Audio[0]), len(full.Audio[0]))
	}
	for i := range full.Audio[0] {
		if full.Audio[0][i] != prev.Audio[0][i] || full.Audio[1][i] != prev.Audio[1][i] {
			t.Fatalf("preview differs from render at %d", i)
		}
	}
	if full.LUFS != prev.LUFS {
		t.Errorf("LUFS %v vs %v", full.LUFS, prev.LUFS)
	}
}

func TestOriginal(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Sources[0].GainDb = -6
	r, err := NewEngine().Original(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.Audio[0]); n < 3*48000-100 || n > 3*48000+100 {
		t.Fatalf("length %d", n)
	}
	// モノラル素材なので左右は同じ(バイノーラル化されていない)
	for i := range r.Audio[0] {
		if r.Audio[0][i] != r.Audio[1][i] {
			t.Fatal("original should be dual mono")
		}
	}
}

// デコードは、その結果を使う段が必要なときだけ行う(キャッシュに当たる変更ではデコードしない)。
func TestDecodeOnlyWhenNeeded(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	p.Venue.Preset = "livehouse"
	e := NewEngine()
	preview := func(q project.Project) {
		if _, err := e.Preview(context.Background(), q, nil); err != nil {
			t.Fatal(err)
		}
	}
	preview(p)
	if n := e.decodes.Load(); n != 1 {
		t.Fatalf("first preview decoded %d times", n)
	}
	for name, mod := range map[string]func(*project.Project){
		"reverb.mix":   func(q *project.Project) { q.Reverb.Mix = 0.7 },
		"listener":     func(q *project.Project) { q.Listener.X = 2 },
		"reverb.decay": func(q *project.Project) { q.Reverb.DecayScale = 0.6 },
	} {
		q := p.Clone()
		mod(&q)
		preview(q)
		if n := e.decodes.Load(); n != 1 {
			t.Errorf("%s: decoded again (%d)", name, n)
		}
	}
	q := p.Clone()
	q.PA.LowCutHz = 150
	preview(q)
	if n := e.decodes.Load(); n != 2 {
		t.Errorf("pa change should decode once more, total %d", n)
	}
}
