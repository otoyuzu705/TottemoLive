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

	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
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
		Reverb:   reverb(0.2, 5, 9000, 1.2, 3),
		RT60Sec:  0.35},
	{ID: "livehouse", Name: "ライブハウス", WidthM: 12, DepthM: 14,
		Speakers: speakers(3, 2.5),
		Subs:     subs(2),
		Reverb:   reverb(0.25, 8, 7000, 1.3, 3),
		RT60Sec:  0.5},
	{ID: "hall", Name: "ホール", WidthM: 30, DepthM: 40,
		Speakers: speakers(7, 6),
		Subs:     subs(4),
		Reverb:   reverb(0.35, 25, 6500, 1.3, 3),
		RT60Sec:  1.8},
	{ID: "arena", Name: "アリーナ", WidthM: 80, DepthM: 70,
		Speakers: speakers(12, 8),
		Subs:     subs(7),
		Reverb:   reverb(0.35, 40, 8000, 1.3, 3),
		RT60Sec:  2.8},
	{ID: "outdoor", Name: "野外フェス", WidthM: 100, DepthM: 120,
		Speakers: speakers(10, 6),
		Subs:     subs(6),
		Reverb:   reverb(0.12, 90, 7000, 1.0, 0),
		RT60Sec:  0.7},
	{ID: "dome", Name: "ドーム", WidthM: 120, DepthM: 100,
		Speakers: speakers(18, 14),
		Subs:     subs(10),
		Reverb:   reverb(0.4, 70, 5500, 1.4, 3),
		RT60Sec:  3.8},
}

// subs はステージ前の床の左右(中心から ±x m)に置く2発のサブウーファー。
func subs(x float64) []project.Speaker {
	return []project.Speaker{{ID: "SubL", X: -x, Y: 1, Z: 0.3}, {ID: "SubR", X: x, Y: 1, Z: 0.3}}
}

// reverb は会場プリセットの残響の既定値。低域の残響は、左右の相関を1(自然な拡散音場)、
// 境界周波数を250 Hzにして、低域の長さの倍率とレベルだけを会場ごとに決める
// (開けた野外は低域がこもらないので 1.0 倍・0 dB)。
func reverb(mix, preDelayMs, highDampHz, lowDecayScale, lowLevelDb float64) project.Reverb {
	return project.Reverb{
		Mix: mix, PreDelayMs: preDelayMs, DecayScale: 1, HighDampHz: highDampHz,
		LowCoherence: 1, LowDecayScale: lowDecayScale, LowLevelDb: lowLevelDb, LowCrossoverHz: 250,
	}
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

// MaxDistanceM は、この会場の中でスピーカーとリスナーが離れうる最大の距離(m)の上限。
// 客席の端からステージの反対側の端まで(横幅の全体 × 奥行き + ステージ分)と、スピーカーの高さを見込む。
// 伝搬遅延で出力が伸びる長さの上限(各段の出力長の固定)に使う。
func MaxDistanceM(pr Preset) float64 {
	const stageDepthM, maxHeightM = 6, 30
	return math.Sqrt(pr.WidthM*pr.WidthM + (pr.DepthM+stageDepthM)*(pr.DepthM+stageDepthM) + maxHeightM*maxHeightM)
}

// IRSeconds は BuildIR が作る会場IRの長さ(秒、プリディレイを含む)。
// 低域の残響が中高域より長いとき(lowDecayScale > 1)は、そちらに合わせる。
func IRSeconds(pr Preset, r project.Reverb) float64 {
	return pr.RT60Sec*r.DecayScale*math.Max(1, r.LowDecayScale)*irTailMargin + r.PreDelayMs*1e-3
}

// IRの正規化に使う中域の範囲(Hz)。高域ダンプ(2 kHz以上)と低域の残響(境界250 Hz以下)の影響を受けにくい帯域。
const (
	normBandLoHz = 600
	normBandHiHz = 1400
)

// midBandEnergy は、左右のIRの中域(normBandLoHz〜normBandHiHz)のエネルギーの合計。
func midBandEnergy(ir [][]float32, fs float64) float64 {
	total := 0.0
	for _, ch := range ir {
		x := append([]float32(nil), ch...)
		dsp.LR4HighPass(x, fs, normBandLoHz)
		dsp.LR4LowPass(x, fs, normBandHiHz)
		for _, v := range x {
			total += float64(v) * float64(v)
		}
	}
	return total
}

// normBandTarget は、チャンネルあたりのエネルギー1の白色IRが、中域に持つエネルギー(帯域幅 / ナイキスト周波数)。
func normBandTarget(fs float64) float64 {
	return (normBandHiHz - normBandLoHz) / (fs / 2)
}

// BuildIR は会場IR(左右)を作る。r.DecayScale で残響の長さ、r.HighDampHz で高域ダンプ、
// r.PreDelayMs でプリディレイを決める。
//
// 低域(r.LowCrossoverHz 以下)は中高域と別に作る:
//   - 左右の相関 r.LowCoherence: 自然な拡散音場は、耳の間隔が波長より小さい低域では左右の残響が
//     ほぼ同じ信号になる(500 Hz 以下で相関が高い)。独立なノイズだけだと低域まで左右バラバラになり、
//     低音の余韻が軽く広がって重さが出ない。右耳の低域を「左と同じ成分 + 独立な成分」で作り、
//     相関を LowCoherence にする(1 で左右同じ)
//   - 残響の長さ r.LowDecayScale: 実際の会場は低域ほど長く残る
//   - レベル r.LowLevelDb
//
// 中域のエネルギー密度をそろえる(上のIR正規化の説明を参照)ので、残響の長さ・高域ダンプ・低域の残響を変えても、
// 残響の中域のレベルは変わらない。
func BuildIR(pr Preset, r project.Reverb, sr int) [][]float32 {
	rtMain := pr.RT60Sec * r.DecayScale
	rtLow := rtMain * r.LowDecayScale
	n := int(math.Max(rtMain, rtLow) * irTailMargin * float64(sr))
	pre := int(r.PreDelayMs * 1e-3 * float64(sr))
	fade := max(int(irFadeInMs*1e-3*float64(sr)), 1)
	fs := float64(sr)

	h := fnv.New64a()
	h.Write([]byte(pr.ID))
	// 左右それぞれの独立な白色ノイズを、低域と中高域に分ける(LR4で分けると、足し合わせた振幅はフラットのまま)
	var low, high [2][]float32
	for c := 0; c < 2; c++ {
		rng := rand.New(rand.NewSource(int64(h.Sum64()) + int64(c)*7919))
		noise := make([]float32, n)
		for i := range noise {
			noise[i] = float32(rng.Float64()*2 - 1)
		}
		low[c] = append([]float32(nil), noise...)
		dsp.LR4LowPass(low[c], fs, r.LowCrossoverHz)
		dsp.LR4HighPass(noise, fs, r.LowCrossoverHz)
		high[c] = noise
	}
	// 右の低域 = c × 左の低域 + √(1-c²) × 右の独立な低域(どちらも同じ強さなので、相関は c になる)
	c := math.Min(math.Max(r.LowCoherence, 0), 1)
	for i := range low[1] {
		low[1][i] = float32(c*float64(low[0][i]) + math.Sqrt(1-c*c)*float64(low[1][i]))
	}

	gLow := dsp.DbToLin(r.LowLevelDb)
	out := make([][]float32, 2)
	for ch := range out {
		ir := make([]float32, pre+n)
		for i := 0; i < n; i++ {
			t := float64(i) / fs
			envMain := math.Exp(-ln1000 * t / rtMain)
			envLow := math.Exp(-ln1000 * t / rtLow)
			if i < fade {
				f := float64(i) / float64(fade)
				envMain, envLow = envMain*f, envLow*f
			}
			ir[pre+i] = float32(float64(high[ch][i])*envMain + gLow*float64(low[ch][i])*envLow)
		}
		dsp.LowPass(fs, r.HighDampHz).Process(ir)
		out[ch] = ir
	}
	// 中域(normBandLoHz〜normBandHiHz)のエネルギー密度が、エネルギー1の白色IRと同じになるようにそろえる。
	// 全体のエネルギーでそろえると、高域ダンプで高域を削るほど中域が持ち上がり、低域のレベルを上げるほど
	// 中域が下がってしまう(つまみが直感どおりに効かない)。中域を基準にすれば、高域ダンプ・低域の残響
	// ・残響の長さは、残響の中域のレベルを変えずに、それぞれの帯域だけを変える
	if e := midBandEnergy(out, fs) / float64(len(out)); e > 0 {
		g := float32(math.Sqrt(normBandTarget(fs) / e))
		for _, ir := range out {
			for i := range ir {
				ir[i] *= g
			}
		}
	}
	return out
}
