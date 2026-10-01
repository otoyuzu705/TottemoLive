// Package project はプロジェクトファイル(JSON)のデータモデルと読み書きを持つ。
// 座標はステージ中央を原点としたメートル単位。x は右、y は客席側(奥)、z は高さ。
package project

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"

	"livebin/internal/params"
)

const (
	Version = 1

	// 出力は48kHz / 24bit ステレオ固定。
	OutputSampleRate = 48000
	OutputBitDepth   = 24

	RoleVocal   = "vocal"
	RoleBacking = "backing"
	RoleMix     = "mix"
)

type Source struct {
	ID     string  `json:"id"`
	Path   string  `json:"path"`
	Role   string  `json:"role"`
	GainDb float64 `json:"gainDb"`
}

type Speaker struct {
	ID string  `json:"id"`
	X  float64 `json:"x"`
	Y  float64 `json:"y"`
	Z  float64 `json:"z"`
}

type Venue struct {
	Preset   string    `json:"preset"`
	Speakers []Speaker `json:"speakers"`
}

// Listener は座席。YawDeg は 0 でステージ(-y方向)を向き、正で右回り。
type Listener struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Z      float64 `json:"z"`
	YawDeg float64 `json:"yawDeg"`
}

type PA struct {
	LowCutHz        float64 `json:"lowCutHz"`
	HighShelfHz     float64 `json:"highShelfHz"`
	HighShelfDb     float64 `json:"highShelfDb"`
	CompThresholdDb float64 `json:"compThresholdDb"`
	CompRatio       float64 `json:"compRatio"`
	CompAttackMs    float64 `json:"compAttackMs"`
	CompReleaseMs   float64 `json:"compReleaseMs"`
	Drive           float64 `json:"drive"`
}

type Spatial struct {
	HrirSet         string  `json:"hrirSet"`
	DistanceRolloff float64 `json:"distanceRolloff"`
	AirAbsorption   float64 `json:"airAbsorption"`
	DirectLevelDb   float64 `json:"directLevelDb"`
}

type Reverb struct {
	Mix        float64 `json:"mix"`
	PreDelayMs float64 `json:"preDelayMs"`
	DecayScale float64 `json:"decayScale"`
	HighDampHz float64 `json:"highDampHz"`
}

type Keyframe struct {
	T     float64 `json:"t"`
	Cheer float64 `json:"cheer"`
}

type ClapRange struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Crowd struct {
	Density    float64     `json:"density"`
	LevelDb    float64     `json:"levelDb"`
	SpreadM    float64     `json:"spreadM"`
	Seed       int         `json:"seed"`
	Keyframes  []Keyframe  `json:"keyframes"`
	ClapRanges []ClapRange `json:"clapRanges"`
}

type Output struct {
	SampleRate  int     `json:"sampleRate"`
	BitDepth    int     `json:"bitDepth"`
	TargetLufs  float64 `json:"targetLufs"`
	CeilingDbTp float64 `json:"ceilingDbTp"`
}

type Project struct {
	Version  int      `json:"version"`
	Sources  []Source `json:"sources"`
	Venue    Venue    `json:"venue"`
	Listener Listener `json:"listener"`
	PA       PA       `json:"pa"`
	Spatial  Spatial  `json:"spatial"`
	Reverb   Reverb   `json:"reverb"`
	Crowd    Crowd    `json:"crowd"`
	Output   Output   `json:"output"`
}

// DefaultVenue は既定の会場(アリーナ)の設定。venueパッケージのアリーナプリセットと一致させる(テストで確認)。
func DefaultVenue() Venue {
	return Venue{
		Preset: "arena",
		Speakers: []Speaker{
			{ID: "L", X: -12, Y: 0, Z: 8},
			{ID: "R", X: 12, Y: 0, Z: 8},
		},
	}
}

// New は全パラメーターが既定値のプロジェクトを返す。
func New() Project {
	p := Project{
		Version:  Version,
		Sources:  []Source{},
		Venue:    DefaultVenue(),
		Listener: Listener{X: 0, Y: 25, Z: 1.2},
		Crowd:    Crowd{Keyframes: []Keyframe{}, ClapRanges: []ClapRange{}},
	}
	if err := params.SetDefaults(&p); err != nil {
		panic(err) // 表とProjectの不一致はテストで検出される
	}
	p.Normalize()
	return p
}

// Normalize は全パラメーターを範囲に丸め、出力形式を固定値にそろえ、
// 客席タイムラインを整える(キーフレームは時刻順・強さ0〜1、手拍子区間は時刻順で重なりを結合)。
func (p *Project) Normalize() {
	_ = params.Clamp(p)
	p.Output.SampleRate = OutputSampleRate
	p.Output.BitDepth = OutputBitDepth
	p.Crowd.Keyframes = normalizeKeyframes(p.Crowd.Keyframes)
	p.Crowd.ClapRanges = normalizeClapRanges(p.Crowd.ClapRanges)
}

func normalizeKeyframes(kf []Keyframe) []Keyframe {
	out := make([]Keyframe, 0, len(kf))
	for _, k := range kf {
		if math.IsNaN(k.T) || math.IsNaN(k.Cheer) || math.IsInf(k.T, 0) {
			continue
		}
		out = append(out, Keyframe{T: math.Max(k.T, 0), Cheer: math.Min(math.Max(k.Cheer, 0), 1)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}

func normalizeClapRanges(rs []ClapRange) []ClapRange {
	valid := make([]ClapRange, 0, len(rs))
	for _, r := range rs {
		if math.IsNaN(r.Start) || math.IsNaN(r.End) || math.IsInf(r.Start, 0) || math.IsInf(r.End, 0) {
			continue
		}
		r.Start = math.Max(r.Start, 0)
		if r.End > r.Start {
			valid = append(valid, r)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Start < valid[j].Start })
	out := make([]ClapRange, 0, len(valid))
	for _, r := range valid {
		if n := len(out); n > 0 && r.Start <= out[n-1].End {
			out[n-1].End = math.Max(out[n-1].End, r.End)
			continue
		}
		out = append(out, r)
	}
	return out
}

// Clone はスライスも含めて複製する。
func (p Project) Clone() Project {
	p.Sources = append([]Source{}, p.Sources...)
	p.Venue.Speakers = append([]Speaker{}, p.Venue.Speakers...)
	p.Crowd.Keyframes = append([]Keyframe{}, p.Crowd.Keyframes...)
	p.Crowd.ClapRanges = append([]ClapRange{}, p.Crowd.ClapRanges...)
	return p
}

// Parse はJSONを読む。欠けた項目は既定値のままにし、範囲に丸める。
func Parse(data []byte) (Project, error) {
	p := New()
	if err := json.Unmarshal(data, &p); err != nil {
		return Project{}, fmt.Errorf("project: %w", err)
	}
	if p.Version > Version {
		return Project{}, fmt.Errorf("project: version %d is newer than supported (%d)", p.Version, Version)
	}
	p.Version = Version
	p.Normalize()
	return p, nil
}

func Load(path string) (Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Project{}, err
	}
	return Parse(data)
}

func Save(path string, p Project) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
