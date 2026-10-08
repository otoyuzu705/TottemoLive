// Package project はプロジェクトファイル(JSON)のデータモデルと読み書きを持つ。
// 座標はステージ中央を原点としたメートル単位。x は右、y は客席側(奥)、z は高さ。
package project

import (
	"encoding/json"
	"fmt"
	"os"

	"tottemolive/internal/params"
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

// Venue は会場。Speakers はメインPA、Subs はサブウーファー(低域だけを受け持つ)の位置。
type Venue struct {
	Preset   string    `json:"preset"`
	Speakers []Speaker `json:"speakers"`
	Subs     []Speaker `json:"subs"`
}

// Listener は座席。YawDeg は 0 でステージ(-y方向)を向き、正で右回り。
type Listener struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Z      float64 `json:"z"`
	YawDeg float64 `json:"yawDeg"`
}

type PA struct {
	// AutoLevel("off" / "on")が on のとき、音源ゲイン後の合計の統合ラウドネスを InputLufs にそろえてからPAを通す。
	AutoLevel       string  `json:"autoLevel"`
	InputLufs       float64 `json:"inputLufs"`
	LowCutHz        float64 `json:"lowCutHz"`
	LowShelfHz      float64 `json:"lowShelfHz"`
	LowShelfDb      float64 `json:"lowShelfDb"`
	PresenceHz      float64 `json:"presenceHz"`
	PresenceDb      float64 `json:"presenceDb"`
	PresenceQ       float64 `json:"presenceQ"`
	HighShelfHz     float64 `json:"highShelfHz"`
	HighShelfDb     float64 `json:"highShelfDb"`
	CompThresholdDb float64 `json:"compThresholdDb"`
	CompRatio       float64 `json:"compRatio"`
	CompAttackMs    float64 `json:"compAttackMs"`
	CompReleaseMs   float64 `json:"compReleaseMs"`
	Drive           float64 `json:"drive"`
}

// Sub はサブウーファー経路の設定。Enabled は "off" / "on"。
// 有効なとき、PA出力を CrossoverHz で分け、低域はサブへ、中高域はメインへ送る。
// LevelDb 0 で、中央に定位した低音について、サブ合計の低域がメインの低域と同じ大きさ。
type Sub struct {
	Enabled     string  `json:"enabled"`
	LevelDb     float64 `json:"levelDb"`
	CrossoverHz float64 `json:"crossoverHz"`
}

type Spatial struct {
	HrirSet         string  `json:"hrirSet"`
	HeadShadow      float64 `json:"headShadow"`
	DistanceRolloff float64 `json:"distanceRolloff"`
	AirAbsorption   float64 `json:"airAbsorption"`
	// 基準点(FOH)での空気吸収の損失を、PAのEQで補う割合(0〜1)と、持ち上げの上限(dB)。直接音と残響の励起に効く。
	AirCompensation      float64 `json:"airCompensation"`
	AirCompensationMaxDb float64 `json:"airCompensationMaxDb"`
	DirectLevelDb        float64 `json:"directLevelDb"`
}

type Reverb struct {
	Mix        float64 `json:"mix"`
	PreDelayMs float64 `json:"preDelayMs"`
	DecayScale float64 `json:"decayScale"`
	HighDampHz float64 `json:"highDampHz"`
	// 低域の残響。LowCrossoverHz 以下について、左右の相関(1で左右同じ信号 = 自然な拡散音場)、
	// 残響の長さの倍率(実際の会場は低域ほど長く残る)、レベルを決める。
	LowCoherence   float64 `json:"lowCoherence"`
	LowDecayScale  float64 `json:"lowDecayScale"`
	LowLevelDb     float64 `json:"lowLevelDb"`
	LowCrossoverHz float64 `json:"lowCrossoverHz"`
	// 高域の残響。HighDecayHz 以上の帯域は、残響の長さが HighDecayScale 倍(1以下)になる。
	// 実際の会場は、空気や壁・客席の吸収で、高域ほど早く減衰する。
	HighDecayScale float64 `json:"highDecayScale"`
	HighDecayHz    float64 `json:"highDecayHz"`
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
	Sub      Sub      `json:"sub"`
	Spatial  Spatial  `json:"spatial"`
	Reverb   Reverb   `json:"reverb"`
	Output   Output   `json:"output"`
}

// DefaultVenue は既定の会場(アリーナ)の設定。venueパッケージのアリーナプリセットと一致させる(テストで確認)。
// サブウーファーはステージ前の床の左右に置く。
func DefaultVenue() Venue {
	return Venue{
		Preset: "arena",
		Speakers: []Speaker{
			{ID: "L", X: -12, Y: 0, Z: 8},
			{ID: "R", X: 12, Y: 0, Z: 8},
		},
		Subs: []Speaker{
			{ID: "SubL", X: -7, Y: 1, Z: 0.3},
			{ID: "SubR", X: 7, Y: 1, Z: 0.3},
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
	}
	if err := params.SetDefaults(&p); err != nil {
		panic(err) // 表とProjectの不一致はテストで検出される
	}
	p.Normalize()
	return p
}

// Normalize は全パラメーターを範囲に丸め、出力形式を固定値にそろえる。
func (p *Project) Normalize() {
	_ = params.Clamp(p)
	p.Output.SampleRate = OutputSampleRate
	p.Output.BitDepth = OutputBitDepth
}

// Clone はスライスも含めて複製する。
func (p Project) Clone() Project {
	p.Sources = append([]Source{}, p.Sources...)
	p.Venue.Speakers = append([]Speaker{}, p.Venue.Speakers...)
	p.Venue.Subs = append([]Speaker{}, p.Venue.Subs...)
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
