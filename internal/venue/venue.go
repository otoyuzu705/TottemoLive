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
	// VolumeM3 は会場の容積(m³)、Q はPAの指向係数(無指向で1。ホーンやラインアレイで約10)。
	// 臨界距離(直接音と残響が同じ大きさになる距離)を決める。
	VolumeM3 float64 `json:"volumeM3"`
	Q        float64 `json:"q"`
}

var presets = []Preset{
	{ID: "club", Name: "クラブ", WidthM: 8, DepthM: 10,
		Speakers: speakers(2, 2),
		Subs:     subs(1.2),
		Reverb:   reverb(5, 9000, 1.2, 3, 0.8),
		RT60Sec:  0.35,
		VolumeM3: 280, Q: 10},
	{ID: "livehouse", Name: "ライブハウス", WidthM: 12, DepthM: 14,
		Speakers: speakers(3, 2.5),
		Subs:     subs(2),
		Reverb:   reverb(8, 7000, 1.3, 3, 0.75),
		RT60Sec:  0.5,
		VolumeM3: 840, Q: 10},
	{ID: "hall", Name: "ホール", WidthM: 30, DepthM: 40,
		Speakers: speakers(7, 6),
		Subs:     subs(4),
		Reverb:   reverb(25, 6500, 1.3, 3, 0.65),
		RT60Sec:  1.8,
		VolumeM3: 14400, Q: 10},
	{ID: "arena", Name: "アリーナ", WidthM: 80, DepthM: 70,
		Speakers: speakers(12, 8),
		Subs:     subs(7),
		Reverb:   reverb(40, 8000, 1.3, 3, 0.6),
		RT60Sec:  2.8,
		VolumeM3: 140000, Q: 10},
	{ID: "outdoor", Name: "野外フェス", WidthM: 100, DepthM: 120,
		Speakers: speakers(10, 6),
		Subs:     subs(6),
		Reverb:   reverb(90, 7000, 1.0, 0, 0.8),
		RT60Sec:  0.7,
		VolumeM3: 1000000, Q: 10},
	{ID: "dome", Name: "ドーム", WidthM: 120, DepthM: 100,
		Speakers: speakers(18, 14),
		Subs:     subs(10),
		Reverb:   reverb(70, 5500, 1.4, 3, 0.55),
		RT60Sec:  3.8,
		VolumeM3: 600000, Q: 10},
}

// subs はステージ前の床の左右(中心から ±x m)に置く2発のサブウーファー。
func subs(x float64) []project.Speaker {
	return []project.Speaker{{ID: "SubL", X: -x, Y: 1, Z: 0.3}, {ID: "SubR", X: x, Y: 1, Z: 0.3}}
}

// NominalMix は reverb.mix の基準値。この値のとき、残響のレベルは会場の物理的な値(臨界距離で直接音と同じ大きさ)になる。
// 会場ごとの残響の多さの違いは、容積・残響時間・指向係数から決まる臨界距離で表すので、
// 会場プリセットの既定値はすべてこの値にする。reverb.mix は、その上の補正(0で無し、1で約+9 dB)になる。
const NominalMix = 0.35

// CriticalDistanceM は臨界距離(m): 直接音と残響の大きさが等しくなる、スピーカーからの距離。
// 0.057·√(Q·V / RT60)(Sabineの式から。V は容積、RT60 は残響時間、Q は指向係数)。
// decayScale で残響を長くすると臨界距離は短くなる(残響が相対的に増える)。
func CriticalDistanceM(pr Preset, decayScale float64) float64 {
	return 0.057 * math.Sqrt(pr.Q*pr.VolumeM3/(pr.RT60Sec*math.Max(decayScale, 0.05)))
}

// reverb は会場プリセットの残響の既定値。高域の残響は、境界周波数を4 kHz、長さの倍率を会場ごとに決める(大きい会場ほど高域が早く減衰する)。低域の残響は、左右の相関を1(自然な拡散音場)、
// 境界周波数を250 Hzにして、低域の長さの倍率とレベルだけを会場ごとに決める
// (開けた野外は低域がこもらないので 1.0 倍・0 dB)。
func reverb(preDelayMs, highDampHz, lowDecayScale, lowLevelDb, highDecayScale float64) project.Reverb {
	return project.Reverb{
		Mix: NominalMix, PreDelayMs: preDelayMs, DecayScale: 1, HighDampHz: highDampHz,
		LowCoherence: 1, LowDecayScale: lowDecayScale, LowLevelDb: lowLevelDb, LowCrossoverHz: 250,
		HighDecayScale: highDecayScale, HighDecayHz: 4000,
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

// highDecayStages は、高域の残響が中域から highDecayScale 倍まで短くなる過程の段数(1オクターブを分けた数)。
const highDecayStages = 3

// highDecayEdges は、中高域を帯域に分ける周波数(Hz): highHz から1オクターブを highDecayStages 等分した境目。
// 帯域は、境目の数 + 1 個(最初が中域、最後が highHz の2倍より上)。
func highDecayEdges(highHz float64) []float64 {
	edges := make([]float64, highDecayStages+1)
	for k := range edges {
		edges[k] = highHz * math.Pow(2, float64(k)/highDecayStages)
	}
	return edges
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
	// 左右それぞれの独立な白色ノイズを、低域と、中高域のいくつかの帯域に分ける(LR4で順に分けると、足し合わせた
	// 振幅はフラットのまま)。中高域は、HighDecayHz までを残響の長さ1倍の中域とし、そこから1オクターブかけて
	// 段階的に短くして、HighDecayHz の2倍より上は HighDecayScale 倍にする(周波数ごとの残響時間の曲線を滑らかにする。
	// 1か所で分けると、境界付近で中域の遅い成分が漏れて、高域の後期の減衰を支配してしまう)
	edges := highDecayEdges(r.HighDecayHz)
	var low [2][]float32
	var bands [2][][]float32 // [チャンネル][帯域][サンプル]。帯域0が中域、最後が最高域
	for c := 0; c < 2; c++ {
		rng := rand.New(rand.NewSource(int64(h.Sum64()) + int64(c)*7919))
		noise := make([]float32, n)
		for i := range noise {
			noise[i] = float32(rng.Float64()*2 - 1)
		}
		low[c] = append([]float32(nil), noise...)
		dsp.LR4LowPass(low[c], fs, r.LowCrossoverHz)
		dsp.LR4HighPass(noise, fs, r.LowCrossoverHz)
		for _, f := range edges {
			part := append([]float32(nil), noise...)
			dsp.LR4LowPass(part, fs, f)
			dsp.LR4HighPass(noise, fs, f)
			bands[c] = append(bands[c], part)
		}
		bands[c] = append(bands[c], noise)
	}
	// 帯域ごとの残響時間(中域を1として、段階的に highScale へ)
	highScale := math.Min(math.Max(r.HighDecayScale, 0.05), 1)
	bandRT := make([]float64, len(bands[0]))
	for j := range bandRT {
		bandRT[j] = rtMain * math.Pow(highScale, float64(j)/float64(len(edges)))
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
		for j, band := range bands[ch] { // 中高域: 帯域ごとの残響時間の減衰
			k := -ln1000 / (bandRT[j] * fs)
			for i := 0; i < n; i++ {
				env := math.Exp(k * float64(i))
				if i < fade {
					env *= float64(i) / float64(fade)
				}
				ir[pre+i] += float32(float64(band[i]) * env)
			}
		}
		kLow := -ln1000 / (rtLow * fs) // 低域
		for i := 0; i < n; i++ {
			env := math.Exp(kLow * float64(i))
			if i < fade {
				env *= float64(i) / float64(fade)
			}
			ir[pre+i] += float32(gLow * float64(low[ch][i]) * env)
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
