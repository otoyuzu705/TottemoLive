// Package venue は会場プリセット(部屋の寸法・スピーカー既定位置・残響の既定値)と、
// 会場IRの生成を持つ。
//
// 会場IRは実測IR(OpenAIRなど)の同梱が素材ごとのライセンス確認待ちのため、
// 当面はプリセットごとの残響時間から合成する(指数減衰する無相関の左右ノイズ)。
package venue

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"

	"livebin/internal/dsp"
	"livebin/internal/project"
)

// Preset は会場1つぶんの定義。
type Preset struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	WidthM   float64           `json:"widthM"` // 客席側の横幅(x は ±WidthM/2)
	DepthM   float64           `json:"depthM"` // ステージ中央から客席最後方までの距離(y は 0〜DepthM)
	Speakers []project.Speaker `json:"speakers"`
	Subs     []project.Speaker `json:"subs"`
	Reverb   project.Reverb    `json:"reverb"`  // 残響パラメーターの既定値
	RT60Sec  float64           `json:"rt60Sec"` // 会場IRの残響時間(decayScale=1)
}

var presets = []Preset{
	{ID: "club", Name: "クラブ", WidthM: 8, DepthM: 10,
		Speakers: speakers(2, 2),
		Subs:     subs(1.2),
		Reverb:   project.Reverb{Mix: 0.2, PreDelayMs: 5, DecayScale: 1, HighDampHz: 9000},
		RT60Sec:  0.35},
	{ID: "livehouse", Name: "ライブハウス", WidthM: 12, DepthM: 14,
		Speakers: speakers(3, 2.5),
		Subs:     subs(2),
		Reverb:   project.Reverb{Mix: 0.25, PreDelayMs: 8, DecayScale: 1, HighDampHz: 7000},
		RT60Sec:  0.5},
	{ID: "hall", Name: "ホール", WidthM: 30, DepthM: 40,
		Speakers: speakers(7, 6),
		Subs:     subs(4),
		Reverb:   project.Reverb{Mix: 0.35, PreDelayMs: 25, DecayScale: 1, HighDampHz: 6500},
		RT60Sec:  1.8},
	{ID: "arena", Name: "アリーナ", WidthM: 80, DepthM: 70,
		Speakers: speakers(12, 8),
		Subs:     subs(7),
		Reverb:   project.Reverb{Mix: 0.35, PreDelayMs: 40, DecayScale: 1, HighDampHz: 8000},
		RT60Sec:  2.8},
	{ID: "outdoor", Name: "野外フェス", WidthM: 100, DepthM: 120,
		Speakers: speakers(10, 6),
		Subs:     subs(6),
		Reverb:   project.Reverb{Mix: 0.12, PreDelayMs: 90, DecayScale: 1, HighDampHz: 7000},
		RT60Sec:  0.7},
	{ID: "dome", Name: "ドーム", WidthM: 120, DepthM: 100,
		Speakers: speakers(18, 14),
		Subs:     subs(10),
		Reverb:   project.Reverb{Mix: 0.4, PreDelayMs: 70, DecayScale: 1, HighDampHz: 5500},
		RT60Sec:  3.8},
}

// subs はステージ前の床の左右(中心から ±x m)に置く2発のサブウーファー。
func subs(x float64) []project.Speaker {
	return []project.Speaker{{ID: "SubL", X: -x, Y: 1, Z: 0.3}, {ID: "SubR", X: x, Y: 1, Z: 0.3}}
}

func speakers(x, z float64) []project.Speaker {
	return []project.Speaker{{ID: "L", X: -x, Y: 0, Z: z}, {ID: "R", X: x, Y: 0, Z: z}}
}

// List は会場プリセット一覧(コピー)を返す。
func List() []Preset {
	out := make([]Preset, len(presets))
	for i, p := range presets {
		p.Speakers = append([]project.Speaker(nil), p.Speakers...)
		p.Subs = append([]project.Speaker(nil), p.Subs...)
		out[i] = p
	}
	return out
}

func Get(id string) (Preset, bool) {
	for _, p := range List() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Apply は会場を切り替える。venue.speakers・venue.subs と reverb.* をプリセットの値で上書きし、
// リスナー位置を部屋の範囲内に収めたプロジェクトを返す。
func Apply(p project.Project, id string) (project.Project, error) {
	pr, ok := Get(id)
	if !ok {
		return p, fmt.Errorf("venue: unknown preset %q", id)
	}
	p = p.Clone()
	p.Venue.Preset = id
	p.Venue.Speakers = pr.Speakers
	p.Venue.Subs = pr.Subs
	p.Reverb = pr.Reverb
	p.Listener.X = math.Min(math.Max(p.Listener.X, -pr.WidthM/2), pr.WidthM/2)
	p.Listener.Y = math.Min(math.Max(p.Listener.Y, 0), pr.DepthM)
	return p, nil
}

// 会場IRの形を決める定数。
const (
	ln1000       = 6.907755278982137 // ln(10^3): 60dB 減衰
	irTailMargin = 1.1               // 減衰が -66dB に達するまで生成
	irFadeInMs   = 3.0               // 立ち上がりのクリックを避ける
)

// IRSeconds は BuildIR が作る会場IRの長さ(秒、プリディレイを含む)。
func IRSeconds(pr Preset, r project.Reverb) float64 {
	return pr.RT60Sec*r.DecayScale*irTailMargin + r.PreDelayMs*1e-3
}

// BuildIR は会場IR(左右)を作る。r.DecayScale で残響の長さ、r.HighDampHz で高域ダンプ、
// r.PreDelayMs でプリディレイを決める。左右合計のエネルギーを1にそろえるので、
// 残響の長さを変えても音量は変わらない。
func BuildIR(pr Preset, r project.Reverb, sr int) [][]float32 {
	rt60 := pr.RT60Sec * r.DecayScale
	n := int(rt60 * irTailMargin * float64(sr))
	pre := int(r.PreDelayMs * 1e-3 * float64(sr))
	fade := max(int(irFadeInMs*1e-3*float64(sr)), 1)

	h := fnv.New64a()
	h.Write([]byte(pr.ID))
	out := make([][]float32, 2)
	total := 0.0
	for c := range out {
		rng := rand.New(rand.NewSource(int64(h.Sum64()) + int64(c)*7919))
		ir := make([]float32, pre+n)
		for i := 0; i < n; i++ {
			t := float64(i) / float64(sr)
			env := math.Exp(-ln1000 * t / rt60)
			if i < fade {
				env *= float64(i) / float64(fade)
			}
			ir[pre+i] = float32((rng.Float64()*2 - 1) * env)
		}
		dsp.LowPass(float64(sr), r.HighDampHz).Process(ir)
		for _, v := range ir {
			total += float64(v) * float64(v)
		}
		out[c] = ir
	}
	if total > 0 {
		g := float32(1 / math.Sqrt(total/2))
		for _, ir := range out {
			for i := range ir {
				ir[i] *= g
			}
		}
	}
	return out
}
