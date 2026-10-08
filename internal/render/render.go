// Package render は音源のデコードから書き出しまでの処理グラフを組み立てて実行する。
//
// 楽曲系統: 音源ごとに(ゲイン → PA質感)を並列に処理 → 合流 →
// 仮想スピーカー(距離減衰・遅延・空気吸収)→ 直接音(HRIR)+ 会場残響(会場IR)。
// 2本をそれぞれのレベルで足し、マスター(ラウドネス → トゥルーピークリミッタ)で仕上げる。
//
// PAより後ろの段(距離・遅延・フィルタ・畳み込み)は線形なので、PAの出力を合流してから処理しても
// 音源ごとに処理して足すのと結果は同じ。非線形なPA質感(コンプ・歪み)だけを音源ごとに並列で回す。
//
// 処理は曲を一定の大きさ(既定65536フレーム)のチャンクに区切って流す。各段は状態を持つ処理器(フィルタ・コンプ・
// 畳み込み・リミッタなど)で、曲全体を一度に処理した場合と、チャンクの大きさに依らずビット単位で同じ結果になる。
// 曲の長さに依らず、メモリはほぼ一定。段の出力(PA・直接音・残響)は、プレビューのキャッシュ用に一時ファイルへ置く。
//
// 書き出しとプレビューは同じ処理(曲全体)を通る。違いは段ごとのキャッシュ(Engine)を使うかどうかだけ。
package render

import (
	"context"
	"errors"
	"fmt"
	"math"

	"tottemolive/internal/analysis"
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
	"tottemolive/internal/venue"
)

// 進捗通知の段階名(Wailsの render:progress の stage と一致)。
const (
	StageDecode  = "decode"
	StageProcess = "process"
	StageEncode  = "encode"
)

const sampleRate = project.OutputSampleRate

// Progress は進捗の通知。ratio は段階内の 0〜1。nil可。複数のgoroutineから呼ばれるので並行呼び出しに耐えること。
type Progress func(stage string, ratio float64)

func (p Progress) report(stage string, ratio float64) {
	if p != nil {
		p(stage, math.Min(math.Max(ratio, 0), 1))
	}
}

// Result はレンダリング結果。音声そのものは Sink に流れる(Audio を持つのは、メモリに集める Render / Original /
// Preview だけ。テストと短い素材用)。
type Result struct {
	Audio      [][]float32 // ステレオ、float32。Sink に流す API では nil
	Frames     int         // 出力のフレーム数
	SampleRate int
	LUFS       float64 // 出力の統合ラウドネス
	// PA は、PA出力(サブ分割の前のバス)の帯域レベルの時系列。プレビュー用のエンジンだけが作る(書き出しでは nil)。
	PA *PASpectrum
}

// PASpectrum はスペクトラム表示で「PAから出た音」を耳に届く音に重ねるためのデータ。
type PASpectrum struct {
	Series *analysis.Series
	// OffsetDb は、PA出力の全体の大きさを、耳に届く出力(マスター後)の全体の大きさにそろえるための値(dB)。
	// 距離減衰・ミックス・ラウドネス調整による全体の音量の違いを除いて、音色(帯域ごとの差)だけを比べられる。
	OffsetDb float64
}

// prepared は検証済みのプロジェクトと、そこから決まる会場・HRIR。
type prepared struct {
	p   project.Project
	pr  venue.Preset
	set spatial.Set
}

func prepare(p project.Project) (*prepared, error) {
	p = p.Clone()
	p.Normalize()
	if len(p.Sources) == 0 {
		return nil, errors.New("render: 音源がありません")
	}
	if len(p.Venue.Speakers) == 0 {
		return nil, errors.New("render: スピーカーがありません")
	}
	pr, ok := venue.Get(p.Venue.Preset)
	if !ok {
		return nil, fmt.Errorf("render: 不明な会場 %q", p.Venue.Preset)
	}
	set, err := spatial.LoadSet(p.Spatial.HrirSet, sampleRate, spatial.SetOptions{HeadShadow: p.Spatial.HeadShadow})
	if err != nil {
		return nil, err
	}
	return &prepared{p: p, pr: pr, set: set}, nil
}

// maxAlignDb は、PA入力のレベル合わせで掛けるゲインの絶対値の上限(dB)。極端に小さい・大きい音源で破綻しないように。
const maxAlignDb = 40

// alignDbFor は、音源ゲイン後の合計の統合ラウドネス lufs を pa.inputLufs にそろえるゲイン(dB)。測れなければ 0。
func alignDbFor(p project.Project, lufs float64) float64 {
	if math.IsInf(lufs, 0) || math.IsNaN(lufs) {
		return 0
	}
	return math.Min(math.Max(p.PA.InputLufs-lufs, -maxAlignDb), maxAlignDb)
}

// paSpectrumOut はPA出力の帯域レベルの時系列と、PA出力(モノ)の2乗平均。
type paSpectrumOut struct {
	series     *analysis.Series
	meanSquare float64
}

// levelOffsetFromMS は、PA出力の2乗平均 paMS を、マスター後の出力(曲の長さぶん、左右平均)の2乗平均 finalMS にそろえる dB。
func levelOffsetFromMS(paMS, finalMS float64) float64 {
	if paMS <= 0 || finalMS <= 0 {
		return 0
	}
	return 10 * math.Log10(finalMS/paMS)
}

// --- 段のキャッシュキー: 「その段が読むパラメーターの値 + 上流の段のキー」 ---

// sourceKeys は各音源のキャッシュキー(ファイルの同一性: パス・サイズ・更新時刻)。ゲインはPA段が読む。
func sourceKeys(sources []project.Source) []string {
	keys := make([]string, len(sources))
	for i, s := range sources {
		keys[i] = hashKey(fileIdentity(s.Path))
	}
	return keys
}

// inputLevelKey は、PA入力のレベル合わせの測定のキー。読むもの: 音源のファイルとゲインだけ。
func inputLevelKey(p project.Project, keys []string) string {
	parts := make([]any, 0, 2*len(keys))
	for i, k := range keys {
		parts = append(parts, k, p.Sources[i].GainDb)
	}
	return hashKey(parts...)
}

// inputLevelStage は、PAの前にレベルをそろえるゲイン(dB)を求める。PAのコンプ(スレッショルド -18 dBFS など)と歪みは
// 入力の絶対レベルで効くので、曲のマスターの音量が違うと同じ設定でも効き方が変わり、『別の曲にそのまま適用できる』
// 音作りプリセットにならない。そこで、音源ゲイン後の合計(バス)の統合ラウドネスを pa.inputLufs にそろえる。
// 全音源に共通のゲインなので、ボーカルと伴奏などの音量バランスは変わらない。音源のゲインは、その上の微調整になる。
//
// 読むもの: 音源のファイルとゲインだけ(測定したラウドネスはキャッシュし、pa.inputLufs の変更ではデコードし直さない)。
// 測定は音源を流しながら行う(曲全体をメモリに持たない)ので、PA段のデコードとは別に、もう一度デコードする。
func (e *Engine) inputLevelStage(ctx context.Context, p project.Project, keys []string, dp *decodeProgress) (float64, error) {
	if p.PA.AutoLevel != "on" {
		return 0, nil
	}
	lufs, err := memo(e.cache, "inputLevel", inputLevelKey(p, keys), func() (float64, error) {
		return e.measureInputLevel(ctx, p, dp)
	})
	if err != nil {
		return 0, err
	}
	return alignDbFor(p, lufs), nil
}

// paKeyFor はPA段のキー。読むもの: 音源のゲイン、レベル合わせのゲイン、pa.* の全項目(autoLevel・inputLufs を含む)、
// 上流のデコード結果(音源のファイル)。
func paKeyFor(p project.Project, keys []string, alignDb float64) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = hashKey(k, p.Sources[i].GainDb, alignDb, paKeyParams(p.PA))
	}
	return hashKey(parts)
}

// paKeyParams は、PA段のキーに入れる pa.*。プレゼンスの量が0なら、周波数とQは音に効かない(フィルタを通さない)ので入れない。
func paKeyParams(pa project.PA) project.PA {
	if pa.PresenceDb == 0 {
		pa.PresenceHz, pa.PresenceQ = 0, 0
	}
	return pa
}

// paSpectrumKeyFor は、PA出力の帯域レベルのキー。読むもの: PAの出力だけ。座席・会場・残響・ミックス・マスターには依らない。
func paSpectrumKeyFor(paKey string) string { return hashKey(paKey, "bands") }

// directKeyFor は直接音のキー。
// 読むもの: リスナー、メイン・サブの位置、sub.*、spatial.hrirSet / headShadow / distanceRolloff / airAbsorption / airCompensation(有効なときだけ airCompensationMaxDb も)、PAの出力。
// (spatial.directLevelDb はミックス段が読む)
// 左右のPAスピーカーを仮想スピーカーとして置き、リスナーの耳に届く直接音を作る。スピーカーが1本ならモノラル和を、
// 2本以上ならチャンネルを順に割り当てる(L,R,L,R...)。サブウーファーが有効なときは、PA出力をクロスオーバーで分け、
// メインには中高域だけを送り、低域はサブ経路(左右のモノ和 → 距離減衰・遅延 → 両耳に同じ信号)で足す。
func directKeyFor(pp *prepared, paKey string) string {
	p := pp.p
	sub := p.Sub
	subs := p.Venue.Subs
	if !subsActive(p) {
		sub = project.Sub{} // サブが無効なら、サブの設定と位置は直接音に影響しない(キーにも入れない)
		subs = nil
	}
	return hashKey(paKey, p.Venue.Preset, p.Listener, p.Venue.Speakers, subs, sub,
		p.Spatial.HrirSet, p.Spatial.HeadShadow, p.Spatial.DistanceRolloff, p.Spatial.AirAbsorption, airCompKey(p))
}

// airCompKey は、直接音のキーに入れる空気吸収の補正の設定。補正が無効(割合0または吸収なし)なら空
// (上限は音に効かないので、無効のときに動かしても再計算しない)。スピーカー・会場の位置は別にキーへ入っている。
func airCompKey(p project.Project) [2]float64 {
	if p.Spatial.AirCompensation <= 0 || p.Spatial.AirAbsorption <= 0 {
		return [2]float64{}
	}
	return [2]float64{p.Spatial.AirCompensation, p.Spatial.AirCompensationMaxDb}
}

// airCompFor は、スピーカー s の空気吸収の補正EQ。補正が無効(割合0・吸収なし)なら空(キーにも効かない)。
func airCompFor(p project.Project, pr venue.Preset, s project.Speaker) spatial.AirComp {
	if p.Spatial.AirCompensation <= 0 || p.Spatial.AirAbsorption <= 0 {
		return spatial.AirComp{}
	}
	ref := fohReference(pr)
	d := math.Sqrt((s.X-ref[0])*(s.X-ref[0]) + (s.Y-ref[1])*(s.Y-ref[1]) + (s.Z-ref[2])*(s.Z-ref[2]))
	return spatial.AirComp{RefDistM: d, Amount: p.Spatial.AirCompensation, MaxBoostDb: p.Spatial.AirCompensationMaxDb}
}

// radiatedAirComp は、残響を励起する音(放射された音)に掛ける補正EQ。メインの基準点までの距離の平均で決める。
// 無効(割合0・吸収なし・スピーカーなし)なら空。座席には依らない。
func radiatedAirComp(p project.Project, pr venue.Preset) spatial.AirComp {
	if len(p.Venue.Speakers) == 0 {
		return spatial.AirComp{}
	}
	sum := 0.0
	for _, s := range p.Venue.Speakers {
		sum += airCompFor(p, pr, s).RefDistM
	}
	c := airCompFor(p, pr, p.Venue.Speakers[0])
	c.RefDistM = sum / float64(len(p.Venue.Speakers))
	return c
}

// reverbIR は会場IRに、空気吸収の補正EQ(有効なとき)を畳み込んだもの。長さは venue.BuildIR と同じ
// (線形位相FIRの群遅延ぶん前を捨て、末尾の同じ長さを捨てる)。無効なら BuildIR の結果そのもの。
// 残響の入力(放射された音)にFIRを掛けるのと同じ結果になる。
func reverbIR(ctx context.Context, pp *prepared) ([][]float32, error) {
	ir := venue.BuildIR(pp.pr, pp.p.Reverb, sampleRate)
	fir := spatial.AirCompFIR(radiatedAirComp(pp.p, pp.pr), pp.p.Spatial.AirAbsorption, sampleRate)
	if fir == nil {
		return ir, nil
	}
	for c := range ir {
		y, err := dsp.Convolve(ctx, ir[c], fir)
		if err != nil {
			return nil, err
		}
		ir[c] = y[spatial.AirGroupDelay : spatial.AirGroupDelay+len(ir[c])]
	}
	return ir, nil
}

// reverbKeyFor は残響のキー。スピーカーから放射された音(radiatedProc)を会場IR(左右)で畳み込む。
// 残響は距離減衰を掛ける前の信号で駆動する(拡散音場のレベルは距離に依らないため)。
// 読むもの: 会場、reverb.preDelayMs / decayScale / highDampHz / low*(低域の残響) / high*(高域の残響)、
// sub.*(サブが有効か・レベル・クロスオーバー。サブの低域も会場を励起するので残響に入る)、
// 空気吸収の補正が有効なときだけ、補正EQ(メインの位置・会場で決まる)と spatial.airAbsorption、PAの出力。
// (reverb.mix はミックス段)
func reverbKeyFor(pp *prepared, paKey string) string {
	r := pp.p.Reverb
	sub := pp.p.Sub
	active := subsActive(pp.p)
	if !active {
		sub = project.Sub{} // サブが無効なら、サブの設定は残響に影響しない(キーにも入れない)
	}
	// 空気吸収の補正(残響の励起に掛かる)は、有効なときだけキーに入れる(無効なら、スピーカー位置と吸収の倍率で再計算しない)
	comp, air := radiatedAirComp(pp.p, pp.pr), 0.0
	if comp != (spatial.AirComp{}) {
		air = pp.p.Spatial.AirAbsorption
	}
	return hashKey(paKey, pp.p.Venue.Preset, r.PreDelayMs, r.DecayScale, r.HighDampHz,
		r.LowCoherence, r.LowDecayScale, r.LowLevelDb, r.LowCrossoverHz, r.HighDecayScale, r.HighDecayHz, active, sub, comp, air)
}

// fohReference は、サブの時間合わせと空気吸収の補正の基準点(現場のFOH: 客席の中央、奥行きの半分、耳の高さ)。
func fohReference(pr venue.Preset) [3]float64 {
	return [3]float64{0, pr.DepthM / 2, 1.2}
}

// subAlignDelays は、サブごとの追加の遅延(サンプル)。座席には依らず、会場とスピーカー・サブの位置で決まる。
func subAlignDelays(p project.Project, pr venue.Preset) []int {
	pos := func(sp []project.Speaker) [][3]float64 {
		out := make([][3]float64, len(sp))
		for i, s := range sp {
			out[i] = [3]float64{s.X, s.Y, s.Z}
		}
		return out
	}
	return spatial.SubAlignDelays(fohReference(pr), pos(p.Venue.Speakers), pos(p.Venue.Subs), sampleRate)
}

// subsActive はサブウーファー経路が有効か(有効にしてあり、サブが1台以上ある)。
func subsActive(p project.Project) bool {
	return p.Sub.Enabled == "on" && len(p.Venue.Subs) > 0
}

// mixGains は、ミックスの2本のゲイン(線形)。
type mixGains struct{ direct, reverb float32 }

// mixGainsFor は、直接音・残響のゲインを決める。
//
// 直接音: spatial.directLevelDb だけ(距離による大きさは、スピーカーごとの距離減衰で既に掛かっている)。
// 残響: 会場の物理的な値を基準にする。残響(拡散音場)の大きさは、リスナーの位置に依らず一定で、直接音は
// 距離に応じて変わる。両者が等しくなる距離が臨界距離で、会場の容積・残響時間・PAの指向係数から決まる
// (venue.CriticalDistanceM)。そこで、残響のゲインを「臨界距離にいるときの、メイン全部の直接音のゲインの和」にする。
// すると、臨界距離より近い席では直接音が主役、遠い席では残響が主役になり、会場による違いも出る。
// reverb.mix は、その上の補正で、基準値(venue.NominalMix)のときに物理的な値、0 で残響なし、1 で約 +9 dB。
// (直接音を (1 - mix) で薄める従来のクロスフェードはやめた。全体の音量はラウドネス調整で決まる)
func mixGainsFor(p project.Project, pr venue.Preset) mixGains {
	dc := venue.CriticalDistanceM(pr, p.Reverb.DecayScale)
	reverb := float64(len(p.Venue.Speakers)) * spatial.Gain(dc, p.Spatial.DistanceRolloff) * p.Reverb.Mix / venue.NominalMix
	return mixGains{
		direct: float32(dsp.DbToLin(p.Spatial.DirectLevelDb)),
		reverb: float32(reverb),
	}
}

// firstArrivalSamples は、リスナーに最初に届く音(いちばん近いメインスピーカーからの直接音)の伝搬遅延(サンプル)。
func firstArrivalSamples(p project.Project) int {
	first := -1
	for _, s := range p.Venue.Speakers {
		_, _, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
		if n := spatial.DelaySamples(d, sampleRate); first < 0 || n < first {
			first = n
		}
	}
	return max(first, 0)
}

// master はラウドネスを目標値に合わせ、トゥルーピークリミッタで仕上げる(buf 全体をその場で)。
// 先行プレビューの窓が曲全体のときに使う。曲全体の書き出し・プレビューは masterPass(流し処理)。
func master(buf [][]float32, sr int, o project.Output) {
	lufs := dsp.IntegratedLUFS(buf, sr)
	if !math.IsInf(lufs, 0) && !math.IsNaN(lufs) {
		g := float32(dsp.DbToLin(o.TargetLufs - lufs))
		for _, ch := range buf {
			for i := range ch {
				ch[i] *= g
			}
		}
	}
	dsp.TruePeakLimit(buf, sr, o.CeilingDbTp)
}
