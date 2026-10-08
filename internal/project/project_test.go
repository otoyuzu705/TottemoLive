package project_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"tottemolive/internal/params"
	"tottemolive/internal/project"
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
	p.Spatial.HrirSet = "nonexistent"
	p.Normalize()
	if p.PA.LowCutHz != 200 || p.Reverb.DecayScale != 0.5 {
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
	path := filepath.Join(t.TempDir(), "p.json")
	if err := project.Save(path, p); err != nil {
		t.Fatal(err)
	}
	q, err := project.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Sources) != 1 || q.Sources[0].GainDb != -2 {
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

func TestSoundPresetRoundTrip(t *testing.T) {
	p := project.New()
	p.Sources = []project.Source{{ID: "a", Path: "/a.wav"}}
	p.Listener.X = 7
	p.PA.LowCutHz = 100
	p.Sub.LevelDb = 8
	sp := project.ExtractSoundPreset(p)

	// 別のプロジェクトに適用しても、素材・座席は変わらない
	q := project.New()
	q.Sources = []project.Source{{ID: "b", Path: "/b.wav"}}
	q.Listener.X = -3
	r := q.ApplySoundPreset(sp)
	if r.PA.LowCutHz != 100 || r.Sub.LevelDb != 8 {
		t.Errorf("preset not applied: %+v", r.PA)
	}
	if r.Sources[0].ID != "b" || r.Listener.X != -3 {
		t.Error("preset must not touch sources/listener")
	}
}

func TestPresetStore(t *testing.T) {
	shipped := fstest.MapFS{"標準.json": {Data: []byte(`{"pa":{"lowCutHz":1}}`)}}
	s := &project.PresetStore{UserDir: filepath.Join(t.TempDir(), "presets"), Shipped: shipped}

	list, err := s.List() // ユーザーディレクトリがなくても出荷時は出る
	if err != nil || len(list) != 1 || !list[0].ReadOnly {
		t.Fatalf("list: %v %v", list, err)
	}
	// 出荷時は範囲に丸めて読める
	sp, err := s.Load("標準")
	if err != nil || sp.PA.LowCutHz != 20 || sp.PA.CompRatio != 3 {
		t.Fatalf("load shipped: %+v %v", sp.PA, err)
	}

	mine := project.ExtractSoundPreset(project.New())
	mine.PA.Drive = 0.9
	if err := s.Save("自作", mine); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("自作")
	if err != nil || got.PA.Drive != 0.9 {
		t.Fatalf("load user: %+v %v", got.PA, err)
	}
	list, _ = s.List()
	if len(list) != 2 || !list[0].ReadOnly || list[1].Name != "自作" || list[1].ReadOnly {
		t.Errorf("list order: %v", list)
	}

	if !errors.Is(s.Save("標準", mine), project.ErrPresetReadOnly) {
		t.Error("overwriting a shipped preset should fail")
	}
	if !errors.Is(s.Delete("標準"), project.ErrPresetReadOnly) {
		t.Error("deleting a shipped preset should fail")
	}
	if err := s.Delete("自作"); err != nil {
		t.Fatal(err)
	}
	if s.Delete("自作") == nil {
		t.Error("deleting a missing preset should fail")
	}
	for _, bad := range []string{"", " ", "../x", "a/b", ".hidden", "x:y", strings.Repeat("あ", 65), " pad"} {
		if s.Save(bad, mine) == nil || project.ValidatePresetName(bad) == nil {
			t.Errorf("bad name accepted: %q", bad)
		}
	}
}

// 客席(歓声・手拍子)の機能を削除する前のプロジェクトとプリセットも、そのまま読める(古い項目は無視する)。
func TestParseProjectWithRemovedCrowd(t *testing.T) {
	p, err := project.Parse([]byte(`{"version":1,"pa":{"lowCutHz":60},"crowd":{"density":0.7,"levelDb":-6,"keyframes":[{"t":0,"cheer":1}],"clapRanges":[{"start":1,"end":2}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.PA.LowCutHz != 60 {
		t.Errorf("pa not read: %+v", p.PA)
	}
	var sp project.SoundPreset
	if err := json.Unmarshal([]byte(`{"pa":{"lowCutHz":60},"crowd":{"density":0.7,"seed":1}}`), &sp); err != nil || sp.PA.LowCutHz != 60 {
		t.Errorf("preset: %v %+v", err, sp.PA)
	}
}

// 旧いプロジェクト(サブ・サブの位置を持たない)を読んでも、既定のサブ設定が入る。
func TestParseOldProjectGetsSubDefaults(t *testing.T) {
	p, err := project.Parse([]byte(`{"version":1,"venue":{"preset":"hall","speakers":[{"id":"L","x":-5,"y":0,"z":4},{"id":"R","x":5,"y":0,"z":4}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Sub.Enabled != "on" || p.Sub.CrossoverHz != 90 || len(p.Venue.Subs) != 2 {
		t.Errorf("sub defaults missing: %+v subs=%d", p.Sub, len(p.Venue.Subs))
	}
}

// 高域シェルフは +12 dB まで受け付け、それを超える値は丸める。下は -12 dB。
func TestHighShelfRange(t *testing.T) {
	for in, want := range map[float64]float64{12: 12, 15: 12, 9: 9, 3.5: 3.5, -12: -12, -20: -12} {
		p := project.New()
		p.PA.HighShelfDb = in
		p.Normalize()
		if p.PA.HighShelfDb != want {
			t.Errorf("highShelfDb %v -> %v, want %v", in, p.PA.HighShelfDb, want)
		}
	}
}

// 低域シェルフは -12〜+9 dB、40〜400 Hz に丸める。既定は 0 dB(何もしない)。
func TestLowShelfRange(t *testing.T) {
	p := project.New()
	if p.PA.LowShelfDb != 0 || p.PA.LowShelfHz != 120 {
		t.Errorf("defaults: %v dB @ %v Hz", p.PA.LowShelfDb, p.PA.LowShelfHz)
	}
	for in, want := range map[float64]float64{9: 9, 12: 9, 4.5: 4.5, -12: -12, -30: -12} {
		q := project.New()
		q.PA.LowShelfDb = in
		q.PA.LowShelfHz = 1
		q.Normalize()
		if q.PA.LowShelfDb != want || q.PA.LowShelfHz != 40 {
			t.Errorf("lowShelfDb %v -> %v (Hz %v), want %v / 40", in, q.PA.LowShelfDb, q.PA.LowShelfHz, want)
		}
	}
	// 旧いプロジェクト(低域シェルフの項目が無い)は既定値で補われ、音が変わらない
	old, err := project.Parse([]byte(`{"version":1,"pa":{"lowCutHz":50}}`))
	if err != nil || old.PA.LowShelfDb != 0 {
		t.Errorf("old project: %+v %v", old.PA, err)
	}
}
