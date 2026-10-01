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
// 書き出しとプレビューは同じ処理を通る。違いは処理範囲(曲全体か、手前に余分を付けた区間か)と、
// 段ごとのキャッシュ(Engine)を使うかどうかだけ。
package render

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"livebin/internal/audio"
	"livebin/internal/crowd"
	"livebin/internal/dsp"
	"livebin/internal/project"
	"livebin/internal/spatial"
	"livebin/internal/venue"
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
}

// window は処理範囲。full なら曲全体(残響の尾を含む)。
// そうでなければ、曲の start サンプル目から n サンプルを出力し、その手前の pre サンプルを
// 残響の立ち上がりのために余分に処理して捨てる。
type window struct {
	full  bool
	start int
	pre   int
	n     int
}

// Render はプロジェクト全体をレンダリングする(キャッシュなし)。
func Render(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	return (&Engine{}).run(ctx, p, window{full: true}, prog)
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
func (e *Engine) run(ctx context.Context, p project.Project, w window, prog Progress) (*Result, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	p = pp.p

	// 1. デコード(並列)
	srcs, err := e.decodeStage(ctx, p.Sources, w, prog)
	if err != nil {
		return nil, err
	}

	// 2. 処理
	steps := newSteps(prog, StageProcess, len(srcs)+4)
	pa, err := e.paStage(ctx, p, srcs, steps)
	if err != nil {
		return nil, err
	}
	bus := sumBus(pa.bufs)

	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	total := w.pre + w.n
	if w.full {
		total = len(bus[0]) + len(ir[0])
	}

	// 3つの系統は互いに独立なので並列に回す
	var direct, reverb, crowdSig [][]float32
	var derr, rerr, cerr error
	var wg sync.WaitGroup
	wg.Add(3)
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
		crowdSig, cerr = e.crowdStage(ctx, pp, w, total)
		steps.done()
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(derr, rerr, cerr); err != nil {
		return nil, err
	}

	// 区間の手前の余分は、残響の立ち上がりを作るためだけに処理したので、ここで捨てる。
	// ラウドネスはこの区間だけで測る(曲全体との差は既知の制約)
	if !w.full {
		direct, reverb, crowdSig = crop(direct, w.pre, w.n), crop(reverb, w.pre, w.n), crop(crowdSig, w.pre, w.n)
	}
	out := mix(p, direct, reverb, crowdSig)
	master(out, sampleRate, p.Output)
	steps.done()
	return &Result{Audio: out, SampleRate: sampleRate, LUFS: dsp.IntegratedLUFS(out, sampleRate)}, nil
}

func crop(buf [][]float32, pre, n int) [][]float32 {
	out := make([][]float32, len(buf))
	for c, ch := range buf {
		out[c] = ch[pre : pre+n]
	}
	return out
}

// stageOut は段の出力とそのキャッシュキー。bufs は読み取り専用(キャッシュと共有される)。
type stageOut struct {
	key  string
	bufs [][]float32
}

// decodeStage は全音源を並列にデコードする。キャッシュキーはファイルの同一性と処理範囲。
func (e *Engine) decodeStage(ctx context.Context, sources []project.Source, w window, prog Progress) ([]stageOut, error) {
	out := make([]stageOut, len(sources))
	errs := make([]error, len(sources))
	ratios := make([]float64, len(sources))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 読むもの: ファイル(パス・サイズ・更新時刻)と処理範囲だけ。ゲインはPA段が読む
			key := hashKey(fileIdentity(s.Path), w)
			bufs, err := memo(e.cache, fmt.Sprintf("decode:%d", i), key, func() ([][]float32, error) {
				return decodeSource(ctx, s.Path, w, func(r float64) {
					mu.Lock()
					ratios[i] = r
					sum := 0.0
					for _, v := range ratios {
						sum += v
					}
					mu.Unlock()
					prog.report(StageDecode, sum/float64(len(ratios)))
				})
			})
			out[i] = stageOut{key: key, bufs: bufs}
			errs[i] = err
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	prog.report(StageDecode, 1)
	return out, nil
}

// decodeSource は音源を window の範囲でデコードする。
// 区間のときは [start-pre, start+n) を取り出し、曲頭より前・ファイル終端より後ろは無音で埋めて長さ pre+n にそろえる。
func decodeSource(ctx context.Context, path string, w window, progress func(float64)) ([][]float32, error) {
	if w.full {
		return audio.Decode(ctx, path, sampleRate, progress)
	}
	total := w.pre + w.n
	from := w.start - w.pre
	lead := 0
	if from < 0 {
		lead, from = -from, 0
	}
	out := [][]float32{make([]float32, total), make([]float32, total)}
	want := total - lead
	if want <= 0 {
		return out, nil
	}
	seg, err := audio.DecodeRange(ctx, path, sampleRate, float64(from)/sampleRate, float64(want)/sampleRate)
	if err != nil {
		return nil, err
	}
	for c := range out {
		copy(out[c][lead:], seg[c])
	}
	progress(1)
	return out, nil
}

// paResult は PA段の出力(音源ごと)と、全音源ぶんを合わせたキー。
type paResult struct {
	key  string
	bufs [][][]float32
}

// paStage は音源ごとに(ゲイン → PA質感)を並列に処理する。
// 読むもの: 音源のゲイン、pa.* の全項目、上流のデコード結果。
func (e *Engine) paStage(ctx context.Context, p project.Project, srcs []stageOut, steps *steps) (paResult, error) {
	res := paResult{bufs: make([][][]float32, len(srcs))}
	keys := make([]string, len(srcs))
	errs := make([]error, len(srcs))
	var wg sync.WaitGroup
	for i := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer steps.done()
			keys[i] = hashKey(srcs[i].key, p.Sources[i].GainDb, p.PA)
			res.bufs[i], errs[i] = memo(e.cache, fmt.Sprintf("pa:%d", i), keys[i], func() ([][]float32, error) {
				buf := cloneBuf(srcs[i].bufs) // applyPAはその場で書き換えるので、キャッシュ内のデコード結果は触らない
				applyPA(buf, sampleRate, p.Sources[i].GainDb, p.PA)
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

func cloneBuf(b [][]float32) [][]float32 {
	out := make([][]float32, len(b))
	for c := range b {
		out[c] = append([]float32(nil), b[c]...)
	}
	return out
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

// applyPA は音源にゲインを掛けてPA質感(低域カット → 高域シェルフ → コンプ → 歪み)を付ける。
func applyPA(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	g := float32(dsp.DbToLin(gainDb))
	for _, ch := range buf {
		for i := range ch {
			ch[i] *= g
		}
		dsp.HighPass(float64(sr), pa.LowCutHz).Process(ch)
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
// 読むもの: リスナー、スピーカー位置、spatial.hrirSet / distanceRolloff / airAbsorption、PAの出力、長さ。
// (spatial.directLevelDb はミックス段が読む)
func (e *Engine) directStage(ctx context.Context, pp *prepared, bus [][]float32, paKey string, total int) ([][]float32, error) {
	p := pp.p
	key := hashKey(paKey, total, p.Listener, p.Venue.Speakers,
		p.Spatial.HrirSet, p.Spatial.DistanceRolloff, p.Spatial.AirAbsorption)
	return memo(e.cache, "direct", key, func() ([][]float32, error) {
		spk := p.Venue.Speakers
		res := make([][][]float32, len(spk))
		errs := make([]error, len(spk))
		var wg sync.WaitGroup
		for i, s := range spk {
			wg.Add(1)
			go func() {
				defer wg.Done()
				feed := speakerFeed(bus, i, len(spk))
				az, el, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
				res[i], errs[i] = spatial.Direct(ctx, feed, sampleRate, d, az, el, pp.set, spatial.DirectParams{
					Rolloff: p.Spatial.DistanceRolloff, AirAbsorption: p.Spatial.AirAbsorption,
				})
			}()
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			return nil, err
		}
		out := [][]float32{make([]float32, total), make([]float32, total)}
		for _, r := range res {
			for c := range out {
				addInto(out[c], r[c])
			}
		}
		return out, nil
	})
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

// reverbStage はPA出力のモノラル和を会場IR(左右)で畳み込む。
// 残響は距離減衰を掛ける前の信号で駆動する(拡散音場のレベルは距離に依らないため)。
// 読むもの: 会場、reverb.preDelayMs / decayScale / highDampHz、PAの出力、長さ。(reverb.mix はミックス段)
func (e *Engine) reverbStage(ctx context.Context, pp *prepared, bus, ir [][]float32, paKey string, total int) ([][]float32, error) {
	r := pp.p.Reverb
	key := hashKey(paKey, total, pp.p.Venue.Preset, r.PreDelayMs, r.DecayScale, r.HighDampHz)
	return memo(e.cache, "reverb", key, func() ([][]float32, error) {
		mono := make([]float32, len(bus[0]))
		for i := range mono {
			mono[i] = (bus[0][i] + bus[1][i]) / 2
		}
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

// crowdStage は客席系統。処理範囲の先頭が曲の何サンプル目かを渡して、絶対時刻で生成する。
// 読むもの: crowd.density / spreadM / seed / keyframes / clapRanges、リスナー、spatial.hrirSet、範囲。
// (crowd.levelDb はミックス段)
func (e *Engine) crowdStage(ctx context.Context, pp *prepared, w window, total int) ([][]float32, error) {
	p := pp.p
	start := w.start - w.pre
	if w.full {
		start = 0
	}
	c := p.Crowd
	key := hashKey(total, start, c.Density, c.SpreadM, c.Seed, c.Keyframes, c.ClapRanges, p.Listener, p.Spatial.HrirSet)
	return memo(e.cache, "crowd", key, func() ([][]float32, error) {
		return crowd.Render(ctx, c, p.Listener, pp.set, sampleRate, start, total)
	})
}

// mix は直接音・残響・客席をそれぞれのレベルで足す。
// 直接音は (1-reverb.mix)、残響は reverb.mix で配分する。
func mix(p project.Project, direct, reverb, crowdSig [][]float32) [][]float32 {
	dg := float32(dsp.DbToLin(p.Spatial.DirectLevelDb) * (1 - p.Reverb.Mix))
	rg := float32(p.Reverb.Mix)
	cg := float32(dsp.DbToLin(p.Crowd.LevelDb))
	out := make([][]float32, 2)
	for c := range out {
		out[c] = make([]float32, len(direct[c]))
		for i := range out[c] {
			out[c][i] = direct[c][i]*dg + reverb[c][i]*rg + crowdSig[c][i]*cg
		}
	}
	return out
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
