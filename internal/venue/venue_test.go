package venue

import (
	"math"
	"reflect"
	"testing"

	"livebin/internal/project"
)

func TestDefaultVenueMatchesArenaPreset(t *testing.T) {
	arena, _ := Get("arena")
	d := project.DefaultVenue()
	if d.Preset != "arena" || !reflect.DeepEqual(d.Speakers, arena.Speakers) || !reflect.DeepEqual(d.Subs, arena.Subs) {
		t.Errorf("project default venue differs from arena preset")
	}
	// 既定のリスナー(0,25)が部屋の中、既定の残響が表の既定値と一致
	p := project.New()
	if p.Reverb != arena.Reverb {
		t.Errorf("reverb default %+v vs arena %+v", p.Reverb, arena.Reverb)
	}
}

func TestApply(t *testing.T) {
	p := project.New()
	p.Listener.X, p.Listener.Y = 40, 60
	q, err := Apply(p, "livehouse")
	if err != nil {
		t.Fatal(err)
	}
	if q.Venue.Preset != "livehouse" || q.Venue.Speakers[1].X != 3 || q.Venue.Subs[1].X != 2 || q.Reverb.PreDelayMs != 8 {
		t.Errorf("not applied: %+v", q.Venue)
	}
	if q.Listener.X != 6 || q.Listener.Y != 14 {
		t.Errorf("listener not clamped: %+v", q.Listener)
	}
	if p.Venue.Preset != "arena" || p.Venue.Speakers[0].X != -12 {
		t.Error("input project was modified")
	}
	if _, err := Apply(p, "nope"); err == nil {
		t.Error("unknown preset accepted")
	}
	// プリセットの残響値は範囲内
	for _, pr := range List() {
		q := project.New()
		q.Reverb = pr.Reverb
		q.Normalize()
		if q.Reverb != pr.Reverb {
			t.Errorf("%s: reverb defaults out of range", pr.ID)
		}
	}
}

func TestBuildIR(t *testing.T) {
	const sr = 48000
	pr, _ := Get("hall")
	r := pr.Reverb
	ir := BuildIR(pr, r, sr)
	if len(ir) != 2 || len(ir[0]) != len(ir[1]) {
		t.Fatal("stereo IR expected")
	}
	// 左右合計のエネルギーが1
	e := 0.0
	for _, c := range ir {
		for _, v := range c {
			e += float64(v) * float64(v)
		}
	}
	if math.Abs(e/2-1) > 1e-3 {
		t.Errorf("energy %v", e/2)
	}
	// プリディレイぶんは無音
	pre := int(r.PreDelayMs * 1e-3 * sr)
	for i := 0; i < pre; i++ {
		if ir[0][i] != 0 {
			t.Fatal("pre-delay not silent")
		}
	}
	// decayScale を上げると長くなる
	r2 := r
	r2.DecayScale = 1.2
	if len(BuildIR(pr, r2, sr)[0]) <= len(ir[0]) {
		t.Error("decayScale should lengthen IR")
	}
	// 左右は無相関
	dot := 0.0
	for i := range ir[0] {
		dot += float64(ir[0][i]) * float64(ir[1][i])
	}
	if math.Abs(dot/e) > 0.05 {
		t.Errorf("L/R correlated: %v", dot/e)
	}
	// 決定的
	if !reflect.DeepEqual(ir, BuildIR(pr, r, sr)) {
		t.Error("IR is not deterministic")
	}
}
