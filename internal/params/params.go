// Package params は音作りパラメーターの定義表と、Pathによる読み書き・範囲への丸めを持つ。
// UI・CLI・値の検証・既定値の共通の元になる。Projectの型には依存せず、
// jsonタグを辿るリフレクションで任意の構造体を扱う。
package params

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// ParamSpec は1つの音作りパラメーターの定義。
type ParamSpec struct {
	Path     string   `json:"path"`    // Project内の位置。例 "pa.lowCutHz"
	Label    string   `json:"label"`   // UIの表示名
	Group    string   `json:"group"`   // pa / spatial / reverb / crowd / master
	Kind     string   `json:"kind"`    // "float" / "int" / "enum"
	Unit     string   `json:"unit"`    // "Hz" "dB" "ms" など
	Min      float64  `json:"min"`     //
	Max      float64  `json:"max"`     //
	Step     float64  `json:"step"`    //
	Default  float64  `json:"default"` // enumのときはOptionsの添字
	Scale    string   `json:"scale"`   // "linear" / "log"(周波数や時間はlog)
	Options  []string `json:"options"` // enumの選択肢
	Advanced bool     `json:"advanced"`
}

const (
	KindFloat = "float"
	KindInt   = "int"
	KindEnum  = "enum"
)

func f(path, label, group, unit string, min, max, step, def float64) ParamSpec {
	return ParamSpec{Path: path, Label: label, Group: group, Kind: KindFloat, Unit: unit,
		Min: min, Max: max, Step: step, Default: def, Scale: "linear"}
}

func logScale(s ParamSpec) ParamSpec { s.Scale = "log"; return s }

func advanced(s ParamSpec) ParamSpec { s.Advanced = true; return s }

// specs が音作りパラメーターの表。範囲と既定値は設計書の表が正で、M2で聴きながら見直す前提の仮の値。
// 残響の既定値(preDelayMs, highDampHz)は会場プリセットごとに上書きされる。
var specs = []ParamSpec{
	logScale(f("pa.lowCutHz", "低域カット", "pa", "Hz", 20, 200, 1, 35)),
	logScale(f("pa.lowShelfHz", "低域シェルフ周波数", "pa", "Hz", 40, 400, 5, 120)),
	f("pa.lowShelfDb", "低域シェルフ量", "pa", "dB", -12, 9, 0.5, 0),
	logScale(f("pa.highShelfHz", "高域シェルフ周波数", "pa", "Hz", 2000, 12000, 100, 6000)),
	f("pa.highShelfDb", "高域シェルフ量", "pa", "dB", -12, 6, 0.5, -3),
	f("pa.compThresholdDb", "コンプ スレッショルド", "pa", "dB", -40, 0, 0.5, -18),
	f("pa.compRatio", "コンプ レシオ", "pa", "", 1, 10, 0.1, 3),
	advanced(logScale(f("pa.compAttackMs", "コンプ アタック", "pa", "ms", 1, 100, 1, 10))),
	advanced(logScale(f("pa.compReleaseMs", "コンプ リリース", "pa", "ms", 20, 1000, 10, 150))),
	f("pa.drive", "歪み", "pa", "", 0, 1, 0.01, 0.2),

	{Path: "sub.enabled", Label: "サブウーファー", Group: "sub", Kind: KindEnum,
		Options: []string{"off", "on"}, Default: 1, Scale: "linear"},
	f("sub.levelDb", "サブ レベル", "sub", "dB", -30, 12, 0.5, 3),
	logScale(f("sub.crossoverHz", "クロスオーバー周波数", "sub", "Hz", 50, 150, 1, 90)),

	{Path: "spatial.hrirSet", Label: "HRIRの種類", Group: "spatial", Kind: KindEnum,
		Options: []string{"synthetic"}, Default: 0, Scale: "linear"},
	f("spatial.distanceRolloff", "距離減衰の強さ", "spatial", "", 0, 1.5, 0.05, 1.0),
	f("spatial.airAbsorption", "空気吸収の強さ", "spatial", "", 0, 2, 0.05, 1.0),
	f("spatial.directLevelDb", "直接音レベル", "spatial", "dB", -12, 6, 0.5, 0),

	f("reverb.mix", "残響量", "reverb", "", 0, 1, 0.01, 0.35),
	f("reverb.preDelayMs", "プリディレイ", "reverb", "ms", 0, 150, 1, 40),
	f("reverb.decayScale", "残響の長さ", "reverb", "倍", 0.5, 1.2, 0.01, 1.0),
	logScale(f("reverb.highDampHz", "残響の高域ダンプ", "reverb", "Hz", 2000, 16000, 100, 8000)),
	f("reverb.lowCoherence", "低域の左右の相関", "reverb", "", 0, 1, 0.05, 1),
	f("reverb.lowDecayScale", "低域の残響の長さ", "reverb", "倍", 0.5, 2.5, 0.05, 1.3),
	f("reverb.lowLevelDb", "低域の残響レベル", "reverb", "dB", -12, 12, 0.5, 3),
	advanced(logScale(f("reverb.lowCrossoverHz", "低域の境界周波数", "reverb", "Hz", 80, 500, 5, 250))),

	f("crowd.density", "客席の密度", "crowd", "", 0, 1, 0.01, 0.7),
	f("crowd.levelDb", "客席レベル", "crowd", "dB", -30, 6, 0.5, -6),
	f("crowd.spreadM", "散布半径", "crowd", "m", 2, 30, 0.5, 10),
	{Path: "crowd.seed", Label: "配置の乱数シード", Group: "crowd", Kind: KindInt,
		Min: 0, Max: 999999, Step: 1, Default: 1, Scale: "linear"},

	f("output.targetLufs", "ラウドネス目標", "master", "LUFS", -24, -9, 0.5, -14),
	f("output.ceilingDbTp", "ピーク上限", "master", "dBTP", -3, 0, 0.1, -1),
}

// ListParams は定義表のコピーを返す。
func ListParams() []ParamSpec {
	out := make([]ParamSpec, len(specs))
	for i, s := range specs {
		s.Options = append([]string(nil), s.Options...)
		out[i] = s
	}
	return out
}

// Find はPathから定義を探す。
func Find(path string) (ParamSpec, bool) {
	for _, s := range ListParams() {
		if s.Path == path {
			return s, true
		}
	}
	return ParamSpec{}, false
}

// DefaultValue は既定値を返す。数値は float64、enumは string。
func (s ParamSpec) DefaultValue() any {
	if s.Kind == KindEnum {
		return s.Options[int(s.Default)]
	}
	return s.Default
}

// field はjsonタグのPathを辿ってroot(構造体へのポインタ)内のフィールドを返す。
func field(root any, path string) (reflect.Value, error) {
	v := reflect.ValueOf(root)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return reflect.Value{}, fmt.Errorf("params: root must be a non-nil pointer")
	}
	v = v.Elem()
	for _, part := range strings.Split(path, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("params: %q: %q is not a struct", path, part)
		}
		t := v.Type()
		found := false
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("json"), ",")[0] == part {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return reflect.Value{}, fmt.Errorf("params: path %q not found", path)
		}
	}
	return v, nil
}

// Get はPathの値を返す。数値は float64、文字列は string。
func Get(root any, path string) (any, error) {
	v, err := field(root, path)
	if err != nil {
		return nil, err
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		return v.Float(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), nil
	case reflect.String:
		return v.String(), nil
	}
	return nil, fmt.Errorf("params: %q has unsupported kind %s", path, v.Kind())
}

// Set はPathに値を書く(丸めない)。valは float64 または string。
func Set(root any, path string, val any) error {
	v, err := field(root, path)
	if err != nil {
		return err
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		x, ok := val.(float64)
		if !ok {
			return fmt.Errorf("params: %q expects a number", path)
		}
		v.SetFloat(x)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		x, ok := val.(float64)
		if !ok {
			return fmt.Errorf("params: %q expects a number", path)
		}
		v.SetInt(int64(math.Round(x)))
	case reflect.String:
		x, ok := val.(string)
		if !ok {
			return fmt.Errorf("params: %q expects a string", path)
		}
		v.SetString(x)
	default:
		return fmt.Errorf("params: %q has unsupported kind %s", path, v.Kind())
	}
	return nil
}

// SetString は文字列(CLIの --set 用)をパースしてPathに書き、そのパラメーターを範囲に丸める。
func SetString(root any, path, s string) error {
	spec, ok := Find(path)
	if !ok {
		return fmt.Errorf("params: unknown parameter %q", path)
	}
	var val any = s
	if spec.Kind != KindEnum {
		x, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return fmt.Errorf("params: %q: %q is not a number", path, s)
		}
		val = x
	} else if !contains(spec.Options, s) {
		return fmt.Errorf("params: %q: %q is not one of %v", path, s, spec.Options)
	}
	if err := Set(root, path, val); err != nil {
		return err
	}
	return clampOne(root, spec)
}

// SetDefaults は全パラメーターを既定値にする。
func SetDefaults(root any) error {
	for _, s := range specs {
		if err := Set(root, s.Path, s.DefaultValue()); err != nil {
			return err
		}
	}
	return nil
}

// Clamp は全パラメーターを Min〜Max に丸める。NaNは既定値に戻し、
// 選択肢にないenumも既定値に戻す。
func Clamp(root any) error {
	for _, s := range specs {
		if err := clampOne(root, s); err != nil {
			return err
		}
	}
	return nil
}

func clampOne(root any, s ParamSpec) error {
	cur, err := Get(root, s.Path)
	if err != nil {
		return err
	}
	if s.Kind == KindEnum {
		str, _ := cur.(string)
		if !contains(s.Options, str) {
			return Set(root, s.Path, s.DefaultValue())
		}
		return nil
	}
	x, _ := cur.(float64)
	if math.IsNaN(x) {
		x = s.Default
	}
	x = math.Min(math.Max(x, s.Min), s.Max)
	return Set(root, s.Path, x)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
