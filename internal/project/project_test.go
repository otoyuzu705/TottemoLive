package project_test

import (
	"path/filepath"
	"testing"

	"livebin/internal/params"
	"livebin/internal/project"
)

// 表のすべてのPathがProjectのフィールドに解決でき、既定値が範囲内であること。
func TestAllParamPathsResolve(t *testing.T) {
	p := project.New()
	for _, s := range params.ListParams() {
		v, err := params.Get(&p, s.Path)
		if err != nil {
			t.Errorf("%s: %v", s.Path, err)
			continue
		}
		if s.Kind == params.KindEnum {
			if len(s.Options) == 0 {
				t.Errorf("%s: enum without options", s.Path)
			}
			continue
		}
		x := v.(float64)
		if x < s.Min || x > s.Max {
			t.Errorf("%s: default %v out of [%v,%v]", s.Path, x, s.Min, s.Max)
		}
		if s.Min >= s.Max || s.Step <= 0 {
			t.Errorf("%s: bad spec range/step", s.Path)
		}
		if s.Scale == "log" && s.Min <= 0 {
			t.Errorf("%s: log scale needs Min>0", s.Path)
		}
	}
}

func TestClamp(t *testing.T) {
	p := project.New()
	p.PA.LowCutHz = 5000
	p.Reverb.DecayScale = -1
	p.Crowd.Seed = -4
	p.Spatial.HrirSet = "nonexistent"
	p.Normalize()
	if p.PA.LowCutHz != 200 || p.Reverb.DecayScale != 0.5 || p.Crowd.Seed != 0 {
		t.Errorf("clamp failed: %+v", p)
	}
	if p.Spatial.HrirSet != "synthetic" {
		t.Errorf("enum not reset: %q", p.Spatial.HrirSet)
	}
}

func TestSetString(t *testing.T) {
	p := project.New()
	if err := params.SetString(&p, "pa.lowCutHz", "80"); err != nil || p.PA.LowCutHz != 80 {
		t.Fatalf("set: %v %v", err, p.PA.LowCutHz)
	}
	if err := params.SetString(&p, "pa.lowCutHz", "9999"); err != nil || p.PA.LowCutHz != 200 {
		t.Fatalf("clamp on set: %v %v", err, p.PA.LowCutHz)
	}
	if err := params.SetString(&p, "crowd.seed", "7.4"); err != nil || p.Crowd.Seed != 7 {
		t.Fatalf("int: %v %v", err, p.Crowd.Seed)
	}
	if params.SetString(&p, "pa.nope", "1") == nil {
		t.Error("unknown path accepted")
	}
	if params.SetString(&p, "pa.lowCutHz", "abc") == nil {
		t.Error("non-number accepted")
	}
	if params.SetString(&p, "spatial.hrirSet", "zzz") == nil {
		t.Error("bad enum accepted")
	}
}

func TestSaveLoadRoundTripAndMissingFields(t *testing.T) {
	p := project.New()
	p.Sources = []project.Source{{ID: "vo", Path: "/a.wav", Role: project.RoleVocal, GainDb: -2}}
	p.Crowd.Keyframes = []project.Keyframe{{T: 0, Cheer: 0.9}}
	path := filepath.Join(t.TempDir(), "p.json")
	if err := project.Save(path, p); err != nil {
		t.Fatal(err)
	}
	q, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Sources) != 1 || q.Sources[0].GainDb != -2 || q.Crowd.Keyframes[0].Cheer != 0.9 {
		t.Errorf("round trip mismatch: %+v", q)
	}

	// 欠けた項目は既定値、範囲外は丸め
	q, err = project.Parse([]byte(`{"version":1,"pa":{"lowCutHz":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if q.PA.LowCutHz != 20 || q.PA.CompRatio != 3 {
		t.Errorf("defaults/clamp: %+v", q.PA)
	}
	if _, err := project.Parse([]byte(`{"version":99}`)); err == nil {
		t.Error("future version accepted")
	}
}
