// Package render は音源のデコードから書き出しまでの処理グラフを組み立てて実行する。
//
// 楽曲系統: 音源ごとに(ゲイン → PA質感)を並列に処理 → 合流 →
// 仮想スピーカー(距離減衰・遅延・空気吸収)→ 直接音(HRIR)+ 会場残響(会場IR)。
// 客席系統: 強さカーブ → リスナー周囲に散布(位置ごとにHRIR)。
// 3本をそれぞれのレベルで足し、マスター(ラウドネス → トゥルーピークリミッタ)で仕上げる。
//
// PAより後ろの段(距離・遅延・フィルタ・畳み込み)は線形なので、PAの出力を合流してから処理しても
// 音源ごとに処理して足すのと結果は同じ。非線形なPA質感(コンプ・歪み)だけを音源ごとに並列で回す。
//
// 書き出しとプレビューは同じ処理(曲全体)を通る。違いは段ごとのキャッシュ(Engine)を使うかどうかだけ。
package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"tottemolive/internal/analysis"
	"tottemolive/internal/audio"
	"tottemolive/internal/crowd"
	"tottemolive/internal/dsp"
	"tottemolive/internal/params"
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

// Result はレンダリング結果(ステレオ、float32)。
type Result struct {
	Audio      [][]float32
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

// Render はプロジェクト全体をレンダリングする(キャッシュなし)。
func Render(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	return (&Engine{}).run(ctx, p, prog)
}

// Export はレンダリングして24bit WAVに書き出す。
func Export(ctx context.Context, p project.Project, outPath string, prog Progress) error {
	res, err := Render(ctx, p, prog)
	if err != nil {
		return err
	}
	return audio.EncodeWAV(ctx, outPath, res.Audio, res.SampleRate, func(r float64) {
		prog.report(StageEncode, r)
	})
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
	set, err := spatial.LoadSet(p.Spatial.HrirSet, sampleRate)
	if err != nil {
		return nil, err
	}
	return &prepared{p: p, pr: pr, set: set}, nil
}

// run は処理グラフを実行する。各段は memo を通すので、e.cache があれば
// 「その段が読むパラメーター + 上流の段のキー」が同じ限り再計算しない。
func (e *Engine) run(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	p = pp.p

	// 1. 音源(デコードは、その結果を使う段が必要としたときに行う)
	srcs := e.sources(p.Sources, prog)

	// 2. 処理
	steps := newSteps(prog, StageProcess, len(srcs)+5)
	level, err := e.inputLevelStage(ctx, p, srcs)
	if err != nil {
		return nil, err
	}
	pa, err := e.paStage(ctx, p, srcs, level, steps)
	if err != nil {
		return nil, err
	}
	bus := &lazyBus{bufs: pa.bufs} // 直接音・残響が両方キャッシュに当たるときは、バスを作らない
	songLen := busLen(pa.bufs)

	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	// 各段の出力の長さは、残響パラメーターを範囲の上限まで振っても収まる値に固定する
	// (IRの長さがキーに入ると、残響を動かしたとき直接音・客席まで再計算になるため)。
	// 実際の長さ(曲 + 現在のIRの尾)へは、ミックスの前に切り詰める
	total := songLen + maxTailSamples(pp.pr)
	// 残響は直接音より先に届かない: 最初に届く音(いちばん近いメインスピーカーの直接音)から残響が始まる。
	// プリディレイはそこからの遅れになる
	revDelay := firstArrivalSamples(p)
	outLen := songLen + revDelay + len(ir[0])

	// 4つの系統は互いに独立なので並列に回す
	var direct, reverb, crowdSig [][]float32
	var paSpec paSpectrumOut
	var derr, rerr, cerr, aerr error
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		if e.analyzePA {
			paSpec, aerr = e.paSpectrumStage(ctx, bus, pa.key)
		}
		steps.done()
	}()
	go func() {
		defer wg.Done()
		direct, derr = e.directStage(ctx, pp, bus, pa.key, total)
		steps.done()
	}()
	go func() {
		defer wg.Done()
		reverb, rerr = e.reverbStage(ctx, pp, bus, ir, pa.key, total)
		steps.done()
	}()
	go func() {
		defer wg.Done()
		crowdSig, cerr = e.crowdStage(ctx, pp, total)
		steps.done()
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(derr, rerr, cerr, aerr); err != nil {
		return nil, err
	}

	out := mix(mixGainsFor(p, pp.pr), crop(direct, outLen), crop(reverb, outLen-revDelay), crop(crowdSig, outLen), revDelay)
	master(out, sampleRate, p.Output)
	steps.done()
	res := &Result{Audio: out, SampleRate: sampleRate, LUFS: dsp.IntegratedLUFS(out, sampleRate)}
	if paSpec.series != nil {
		res.PA = &PASpectrum{Series: paSpec.series, OffsetDb: levelOffsetDb(paSpec.meanSquare, out, songLen)}
	}
	return res, nil
}

func crop(buf [][]float32, n int) [][]float32 {
	out := make([][]float32, len(buf))
	for c, ch := range buf {
		out[c] = ch[:min(n, len(ch))]
	}
	return out
}

// maxTailSamples は、残響パラメーターを範囲の上限まで振っても会場IRが収まる長さ。
func maxTailSamples(pr venue.Preset) int {
	pre, _ := params.Find("reverb.preDelayMs")
	decay, _ := params.Find("reverb.decayScale")
	low, _ := params.Find("reverb.lowDecayScale")
	sec := venue.IRSeconds(pr, project.Reverb{PreDelayMs: pre.Max, DecayScale: decay.Max, LowDecayScale: low.Max})
	// 直接音と残響の伝搬遅延(最大の距離ぶん)も見込む
	sec += venue.MaxDistanceM(pr) / spatial.SpeedOfSound
	return int(math.Ceil(sec * sampleRate))
}

// paSpectrumOut はPA出力の帯域レベルの時系列と、PA出力(モノ)の2乗平均。
type paSpectrumOut struct {
	series     *analysis.Series
	meanSquare float64
}

// paSpectrumStage はPA出力(サブ分割の前のバスの左右平均)の帯域レベルを、曲全体について求める。
// 読むもの: PAの出力だけ(キーは PA 段のキーと同じ)。座席・会場・残響・ミックス・マスターには依らない。
func (e *Engine) paSpectrumStage(ctx context.Context, bus *lazyBus, paKey string) (paSpectrumOut, error) {
	return memo(e.cache, "paSpectrum", hashKey(paKey, "bands"), func() (paSpectrumOut, error) {
		in := bus.get()
		mono := analysis.Mono(in[0], in[1])
		s, err := analysis.Compute(ctx, mono, sampleRate)
		if err != nil {
			return paSpectrumOut{}, err
		}
		return paSpectrumOut{series: s, meanSquare: analysis.MeanSquare(mono)}, nil
	})
}

// levelOffsetDb は、PA出力の2乗平均 paMS を、マスター後の出力(曲の長さぶん、左右平均)の2乗平均にそろえる dB。
func levelOffsetDb(paMS float64, out [][]float32, songLen int) float64 {
	n := min(songLen, len(out[0]))
	finalMS := analysis.MeanSquare(analysis.Mono(out[0][:n], out[1][:n]))
	if paMS <= 0 || finalMS <= 0 {
		return 0
	}
	return 10 * math.Log10(finalMS/paMS)
}

// stageOut は上流の段を表す。key はキャッシュキー、load は出力を作る(デコードなど)。
// 出力を必要とする段(キャッシュに当たらなかった段)だけが load を呼ぶ。
type stageOut struct {
	key  string
	load func(ctx context.Context) ([][]float32, error)
}

// sources は各音源のデコードを上流の段として用意する。デコード結果はキャッシュしない
// (ffmpegでのデコードは4分の曲で0.3秒ほどで、キャッシュするとその曲ぶんのメモリを常に占める)。
// キャッシュキーはファイルの同一性(パス・サイズ・更新時刻)で、ゲインはPA段が読む。
func (e *Engine) sources(sources []project.Source, prog Progress) []stageOut {
	out := make([]stageOut, len(sources))
	ratios := make([]float64, len(sources))
	var mu sync.Mutex
	report := func(i int, r float64) {
		mu.Lock()
		ratios[i] = r
		sum := 0.0
		for _, v := range ratios {
			sum += v
		}
		mu.Unlock()
		prog.report(StageDecode, sum/float64(len(ratios)))
	}
	for i, s := range sources {
		out[i] = stageOut{
			key: hashKey(fileIdentity(s.Path)),
			load: func(ctx context.Context) ([][]float32, error) {
				e.decodes.Add(1)
				buf, err := audio.Decode(ctx, s.Path, sampleRate, func(r float64) { report(i, r) })
				if err == nil {
					report(i, 1)
				}
				return buf, err
			},
		}
	}
	return out
}

// paResult は PA段の出力(音源ごと)と、全音源ぶんを合わせたキー。
type paResult struct {
	key  string
	bufs [][][]float32
}

// inputLevel はPA入力のレベル合わせの結果。AlignDb はPAの前に全音源へ共通に掛けるゲイン(dB)。
// Preloaded は、レベルの測定のためにデコードした音源(PA段が再利用する。使ったら nil にする)。
type inputLevel struct {
	AlignDb   float64
	preloaded [][][]float32
}

// inputLevelStage は、PAの前にレベルをそろえるゲインを求める。PAのコンプ(スレッショルド -18 dBFS など)と歪みは
// 入力の絶対レベルで効くので、曲のマスターの音量が違うと同じ設定でも効き方が変わり、『別の曲にそのまま適用できる』
// 音作りプリセットにならない。そこで、音源ゲイン後の合計(バス)の統合ラウドネスを pa.inputLufs にそろえる。
// 全音源に共通のゲインなので、ボーカルと伴奏などの音量バランスは変わらない。音源のゲインは、その上の微調整になる。
//
// 読むもの: 音源のファイルとゲインだけ(測定したラウドネスはキャッシュし、pa.inputLufs の変更ではデコードし直さない)。
// 測定のためにデコードしたときは、その結果をPA段に渡して二重にデコードしない。
func (e *Engine) inputLevelStage(ctx context.Context, p project.Project, srcs []stageOut) (inputLevel, error) {
	if p.PA.AutoLevel != "on" {
		return inputLevel{}, nil
	}
	parts := make([]any, 0, 2*len(srcs))
	for i, s := range srcs {
		parts = append(parts, s.key, p.Sources[i].GainDb)
	}
	key := hashKey(parts...)
	var preloaded [][][]float32
	lufs, err := memo(e.cache, "inputLevel", key, func() (float64, error) {
		bufs := make([][][]float32, len(srcs))
		errs := make([]error, len(srcs))
		var wg sync.WaitGroup
		for i := range srcs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				bufs[i], errs[i] = srcs[i].load(ctx)
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return 0, err
		}
		sum := make([][]float32, 2)
		n := 0
		for _, b := range bufs {
			n = max(n, len(b[0]))
		}
		for c := range sum {
			sum[c] = make([]float32, n)
			for i, b := range bufs {
				g := float32(dsp.DbToLin(p.Sources[i].GainDb))
				for k, v := range b[c] {
					sum[c][k] += v * g
				}
			}
		}
		preloaded = bufs
		return dsp.IntegratedLUFS(sum, sampleRate), nil
	})
	if err != nil {
		return inputLevel{}, err
	}
	lv := inputLevel{preloaded: preloaded}
	if !math.IsInf(lufs, 0) && !math.IsNaN(lufs) {
		lv.AlignDb = math.Min(math.Max(p.PA.InputLufs-lufs, -maxAlignDb), maxAlignDb)
	}
	return lv, nil
}

// maxAlignDb は、PA入力のレベル合わせで掛けるゲインの絶対値の上限(dB)。極端に小さい・大きい音源で破綻しないように。
const maxAlignDb = 40

// paStage は音源ごとに(ゲイン + レベル合わせ → PA質感)を並列に処理する。
// 読むもの: 音源のゲイン、レベル合わせのゲイン、pa.* の全項目(autoLevel・inputLufs を含む)、上流のデコード結果。
func (e *Engine) paStage(ctx context.Context, p project.Project, srcs []stageOut, level inputLevel, steps *steps) (paResult, error) {
	res := paResult{bufs: make([][][]float32, len(srcs))}
	keys := make([]string, len(srcs))
	errs := make([]error, len(srcs))
	var wg sync.WaitGroup
	for i := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer steps.done()
			keys[i] = hashKey(srcs[i].key, p.Sources[i].GainDb, level.AlignDb, p.PA)
			res.bufs[i], errs[i] = memo(e.cache, fmt.Sprintf("pa:%d", i), keys[i], func() ([][]float32, error) {
				var buf [][]float32
				if level.preloaded != nil && level.preloaded[i] != nil {
					buf, level.preloaded[i] = level.preloaded[i], nil // レベルの測定でデコード済み
				} else {
					var err error
					if buf, err = srcs[i].load(ctx); err != nil { // デコード結果は他で使わないので、その場で処理してよい
						return nil, err
					}
				}
				applyPA(buf, sampleRate, p.Sources[i].GainDb+level.AlignDb, p.PA)
				return buf, nil
			})
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return paResult{}, err
	}
	res.key = hashKey(keys)
	return res, errors.Join(errs...)
}

// busLen は音源ごとのPA出力のうち最長のもの(バスの長さ)。
func busLen(bufs [][][]float32) int {
	n := 0
	for _, s := range bufs {
		n = max(n, len(s[0]))
	}
	return n
}

// lazyBus はPA出力の合計(バス)を、最初に必要とされたときに一度だけ作る。
type lazyBus struct {
	once sync.Once
	bufs [][][]float32
	bus  [][]float32
}

func (b *lazyBus) get() [][]float32 {
	b.once.Do(func() { b.bus = sumBus(b.bufs) })
	return b.bus
}

// sumBus は音源ごとのPA出力を足し合わせる。長さは最長の音源にそろえる。
func sumBus(bufs [][][]float32) [][]float32 {
	n := 0
	for _, s := range bufs {
		n = max(n, len(s[0]))
	}
	bus := [][]float32{make([]float32, n), make([]float32, n)}
	for _, s := range bufs {
		for c := range bus {
			for i, v := range s[c] {
				bus[c][i] += v
			}
		}
	}
	return bus
}

// applyPA は音源にゲインを掛けてPA質感(低域カット → 低域シェルフ → 高域シェルフ → コンプ → 歪み)を付ける。
func applyPA(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	g := float32(dsp.DbToLin(gainDb))
	for _, ch := range buf {
		for i := range ch {
			ch[i] *= g
		}
		dsp.HighPass(float64(sr), pa.LowCutHz).Process(ch)
		dsp.LowShelf(float64(sr), pa.LowShelfHz, pa.LowShelfDb).Process(ch)
		dsp.HighShelf(float64(sr), pa.HighShelfHz, pa.HighShelfDb).Process(ch)
	}
	dsp.Compress(buf, sr, dsp.CompParams{
		ThresholdDb: pa.CompThresholdDb, Ratio: pa.CompRatio,
		AttackMs: pa.CompAttackMs, ReleaseMs: pa.CompReleaseMs,
	})
	dsp.Saturate(buf, pa.Drive)
}

// directStage は左右のPAスピーカーを仮想スピーカーとして置き、リスナーの耳に届く直接音を返す。
// スピーカーが1本ならモノラル和を、2本以上ならチャンネルを順に割り当てる(L,R,L,R...)。
// サブウーファーが有効なときは、PA出力をクロスオーバーで分け、メインには中高域だけを送り、
// 低域はサブ経路(左右のモノ和 → 距離減衰・遅延 → 両耳に同じ信号)で足す。
// 読むもの: リスナー、メイン・サブの位置、sub.*、spatial.hrirSet / distanceRolloff / airAbsorption、PAの出力、長さ。
// (spatial.directLevelDb はミックス段が読む)
func (e *Engine) directStage(ctx context.Context, pp *prepared, bus *lazyBus, paKey string, total int) ([][]float32, error) {
	p := pp.p
	sub := p.Sub
	if !subsActive(p) {
		sub = project.Sub{} // サブが無効なら、サブの設定と位置は直接音に影響しない(キーにも入れない)
	}
	subs := p.Venue.Subs
	if !subsActive(p) {
		subs = nil
	}
	key := hashKey(paKey, total, p.Listener, p.Venue.Speakers, subs, sub,
		p.Spatial.HrirSet, p.Spatial.DistanceRolloff, p.Spatial.AirAbsorption)
	return memo(e.cache, "direct", key, func() ([][]float32, error) {
		spk := p.Venue.Speakers
		subOn := subsActive(p)
		in := bus.get()
		out := [][]float32{make([]float32, total), make([]float32, total)}
		// スピーカーごとに並列に計算し、できた順ではなくスピーカーの順に足し込む。
		// 全スピーカーぶんの結果を同時に持たず、足し算の順序も固定になる
		turn := make([]chan struct{}, len(spk))
		for i := range turn {
			turn[i] = make(chan struct{})
		}
		errs := make([]error, len(spk))
		var wg sync.WaitGroup
		for i, s := range spk {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer close(turn[i])
				feed := speakerFeed(in, i, len(spk))
				if subOn {
					// 低域はサブが受け持つので、メインは中高域だけにする(元のバスは他の経路が使うので複製して掛ける)
					feed = append([]float32(nil), feed...)
					dsp.LR4HighPass(feed, sampleRate, p.Sub.CrossoverHz)
				}
				az, el, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
				r, err := spatial.Direct(ctx, feed, sampleRate, d, az, el, pp.set, spatial.DirectParams{
					Rolloff: p.Spatial.DistanceRolloff, AirAbsorption: p.Spatial.AirAbsorption,
				})
				if i > 0 {
					<-turn[i-1]
				}
				if err != nil {
					errs[i] = err
					return
				}
				for c := range out {
					addInto(out[c], r[c])
				}
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		if subOn {
			addSubs(out, in, p)
		}
		return out, nil
	})
}

// addSubs はサブウーファー経路を out(両耳)に足す。
// バスの左右の合計(モノ)の低域を、サブの台数で割って(合計が levelDb になるように)、
// サブごとに距離減衰・遅延を掛けて両耳に同じ信号として足す。
// 左右の合計にするのは、中央に定位した低音(左右同じ信号)がメイン2本でコヒーレントに足される大きさ(+6 dB)に
// 合わせるため。これで levelDb 0 が「メインの低域と同じ大きさ」になる。
func addSubs(out, bus [][]float32, p project.Project) {
	mono := make([]float32, len(bus[0]))
	for i := range mono {
		mono[i] = bus[0][i] + bus[1][i]
	}
	dsp.LR4LowPass(mono, sampleRate, p.Sub.CrossoverHz)
	g := float32(dsp.DbToLin(p.Sub.LevelDb) / float64(len(p.Venue.Subs)))
	for i := range mono {
		mono[i] *= g
	}
	// メインの代表距離(サブの遅延をメインに合わせる基準)
	mains := 0.0
	for _, s := range p.Venue.Speakers {
		_, _, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
		mains += d / float64(len(p.Venue.Speakers))
	}
	for _, s := range p.Venue.Subs {
		_, _, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
		sig := spatial.Sub(mono, sampleRate, d, mains, p.Spatial.DistanceRolloff)
		for c := range out {
			addInto(out[c], sig)
		}
	}
}

// subsActive はサブウーファー経路が有効か(有効にしてあり、サブが1台以上ある)。
func subsActive(p project.Project) bool {
	return p.Sub.Enabled == "on" && len(p.Venue.Subs) > 0
}

// radiatedMono は、スピーカーから放射された音のモノラル表現(会場の残響を励起する音)を返す。
// サブが無効なら バスの左右平均。有効なら メインの高域(クロスオーバーより上)の左右平均 +
// サブの低域(左右の合計のクロスオーバーより下に、サブのレベルを掛けたものの半分)。
// 半分にするのは、サブ経路のモノ合計(左右の和)をメイン2本が出す低域(左右平均)の大きさにそろえるため:
// サブ 0 dB で、分けない場合(左右平均)と同じ大きさになり、サブのレベルを上げると残響の低域もその分増える。
// 実際の会場では、サブが強く鳴るほど部屋の低域の残響も増える。
func radiatedMono(bus [][]float32, active bool, sub project.Sub) []float32 {
	n := len(bus[0])
	mono := make([]float32, n)
	if !active {
		for i := range mono {
			mono[i] = (bus[0][i] + bus[1][i]) / 2
		}
		return mono
	}
	fs := float64(sampleRate)
	tmp := make([]float32, n)
	for c := 0; c < 2; c++ {
		copy(tmp, bus[c])
		dsp.LR4HighPass(tmp, fs, sub.CrossoverHz)
		for i, v := range tmp {
			mono[i] += v / 2
		}
	}
	for i := range tmp {
		tmp[i] = bus[0][i] + bus[1][i]
	}
	dsp.LR4LowPass(tmp, fs, sub.CrossoverHz)
	g := float32(dsp.DbToLin(sub.LevelDb) / 2)
	for i, v := range tmp {
		mono[i] += v * g
	}
	return mono
}

func speakerFeed(bus [][]float32, i, count int) []float32 {
	if count == 1 {
		m := make([]float32, len(bus[0]))
		for k := range m {
			m[k] = (bus[0][k] + bus[1][k]) / 2
		}
		return m
	}
	return bus[i%2]
}

// reverbStage はスピーカーから放射された音(radiatedMono)を会場IR(左右)で畳み込む。
// 残響は距離減衰を掛ける前の信号で駆動する(拡散音場のレベルは距離に依らないため)。
// 読むもの: 会場、reverb.preDelayMs / decayScale / highDampHz / low*(低域の残響)、
// sub.*(サブが有効か・レベル・クロスオーバー。サブの低域も会場を励起するので残響に入る)、PAの出力、長さ。
// (reverb.mix はミックス段)
func (e *Engine) reverbStage(ctx context.Context, pp *prepared, bus *lazyBus, ir [][]float32, paKey string, total int) ([][]float32, error) {
	r := pp.p.Reverb
	sub := pp.p.Sub
	active := subsActive(pp.p)
	if !active {
		sub = project.Sub{} // サブが無効なら、サブの設定は残響に影響しない(キーにも入れない)
	}
	key := hashKey(paKey, total, pp.p.Venue.Preset, r.PreDelayMs, r.DecayScale, r.HighDampHz,
		r.LowCoherence, r.LowDecayScale, r.LowLevelDb, r.LowCrossoverHz, active, sub)
	return memo(e.cache, "reverb", key, func() ([][]float32, error) {
		mono := radiatedMono(bus.get(), active, sub)
		out := [][]float32{make([]float32, total), make([]float32, total)}
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for c := 0; c < 2; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var y []float32
				y, errs[c] = dsp.Convolve(ctx, mono, ir[c])
				addInto(out[c], y)
			}()
		}
		wg.Wait()
		return out, errors.Join(errs...)
	})
}

// crowdStage は客席系統。曲頭からの絶対時刻で生成する。
// 読むもの: crowd.density / spreadM / seed / keyframes / clapRanges、リスナー、spatial.hrirSet、長さ。
// (crowd.levelDb はミックス段)
func (e *Engine) crowdStage(ctx context.Context, pp *prepared, total int) ([][]float32, error) {
	p := pp.p
	c := p.Crowd
	key := hashKey(total, c.Density, c.SpreadM, c.Seed, c.Keyframes, c.ClapRanges, p.Listener, p.Spatial.HrirSet)
	return memo(e.cache, "crowd", key, func() ([][]float32, error) {
		return crowd.Render(ctx, c, p.Listener, pp.set, sampleRate, 0, total)
	})
}

// mixGains は、ミックスの3本のゲイン(線形)。
type mixGains struct{ direct, reverb, crowd float32 }

// mixGainsFor は、直接音・残響・客席のゲインを決める。
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
		crowd:  float32(dsp.DbToLin(p.Crowd.LevelDb)),
	}
}

// mix は直接音・残響・客席を、それぞれのゲインで足す。
func mix(g mixGains, direct, reverb, crowdSig [][]float32, reverbDelay int) [][]float32 {
	out := make([][]float32, 2)
	for c := range out {
		out[c] = make([]float32, len(direct[c]))
		for i := range out[c] {
			out[c][i] = direct[c][i]*g.direct + crowdSig[c][i]*g.crowd
		}
		// 残響は、リスナーに最初の音が届く時刻(reverbDelay)から始まる
		for i, v := range reverb[c] {
			if j := i + reverbDelay; j < len(out[c]) {
				out[c][j] += v * g.reverb
			}
		}
	}
	return out
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

// master はラウドネスを目標値に合わせ、トゥルーピークリミッタで仕上げる。
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

func addInto(dst, src []float32) {
	n := min(len(dst), len(src))
	for i := 0; i < n; i++ {
		dst[i] += src[i]
	}
}

// steps は処理段階の進捗を「完了したタスク数 / 全タスク数」で通知する。
type steps struct {
	mu    sync.Mutex
	n     int
	total int
	stage string
	prog  Progress
}

func newSteps(prog Progress, stage string, total int) *steps {
	prog.report(stage, 0)
	return &steps{prog: prog, stage: stage, total: total}
}

func (s *steps) done() {
	s.mu.Lock()
	s.n++
	r := float64(s.n) / float64(s.total)
	s.mu.Unlock()
	s.prog.report(s.stage, r)
}
