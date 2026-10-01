// Package render は音源のデコードから書き出しまでの処理グラフを組み立てて実行する。
//
// 楽曲系統: 音源ごとに(ゲイン → PA質感)を並列に処理 → 合流 →
// 仮想スピーカー(距離減衰・遅延・空気吸収)→ 直接音(HRIR)+ 会場残響(会場IR)。
// 客席系統: 強さカーブ → リスナー周囲に散布(位置ごとにHRIR)。
// 3本をそれぞれのレベルで足し、マスター(ラウドネス → トゥルーピークリミッタ)で仕上げる。
//
// PAより後ろの段(距離・遅延・フィルタ・畳み込み)は線形なので、PAの出力を合流してから処理しても
// 音源ごとに処理して足すのと結果は同じ。非線形なPA質感(コンプ・歪み)だけを音源ごとに並列で回す。
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

// Render はプロジェクト全体をレンダリングする。
func Render(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
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
	const sr = project.OutputSampleRate
	set, err := spatial.LoadSet(p.Spatial.HrirSet, sr)
	if err != nil {
		return nil, err
	}

	// 1. デコード(並列)
	srcs, err := decodeAll(ctx, p.Sources, sr, prog)
	if err != nil {
		return nil, err
	}

	// 2. 処理
	steps := newSteps(prog, StageProcess, len(srcs)+4)
	bus, err := paStage(ctx, p, srcs, sr, steps)
	if err != nil {
		return nil, err
	}

	ir := venue.BuildIR(pr, p.Reverb, sr)
	total := len(bus[0]) + len(ir[0])

	var direct, reverb, crowdSig [][]float32
	var derr, rerr, cerr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		direct, derr = directStage(ctx, p, bus, sr, set, total)
		steps.done()
	}()
	go func() {
		defer wg.Done()
		reverb, rerr = reverbStage(ctx, bus, ir, total)
		steps.done()
	}()
	go func() {
		defer wg.Done()
		crowdSig, cerr = crowd.Render(ctx, p.Crowd, p.Listener, set, sr, total)
		steps.done()
	}()
	wg.Wait()
	if err := errors.Join(derr, rerr, cerr); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}

	out := mix(p, direct, reverb, crowdSig)
	master(out, sr, p.Output)
	steps.done()
	return &Result{Audio: out, SampleRate: sr, LUFS: dsp.IntegratedLUFS(out, sr)}, nil
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

func decodeAll(ctx context.Context, sources []project.Source, sr int, prog Progress) ([][][]float32, error) {
	out := make([][][]float32, len(sources))
	errs := make([]error, len(sources))
	ratios := make([]float64, len(sources))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, s := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = audio.Decode(ctx, s.Path, sr, func(r float64) {
				mu.Lock()
				ratios[i] = r
				sum := 0.0
				for _, v := range ratios {
					sum += v
				}
				mu.Unlock()
				prog.report(StageDecode, sum/float64(len(ratios)))
			})
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

// paStage は音源ごとに(ゲイン → PA質感)を並列に処理し、合流したバスを返す。
func paStage(ctx context.Context, p project.Project, srcs [][][]float32, sr int, steps *steps) ([][]float32, error) {
	var wg sync.WaitGroup
	for i := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applyPA(srcs[i], sr, p.Sources[i].GainDb, p.PA)
			steps.done()
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n := 0
	for _, s := range srcs {
		n = max(n, len(s[0]))
	}
	bus := [][]float32{make([]float32, n), make([]float32, n)}
	for _, s := range srcs {
		for c := range bus {
			for i, v := range s[c] {
				bus[c][i] += v
			}
		}
	}
	return bus, nil
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
// スピーカーが2本のときは左右チャンネルをそれぞれに、1本ならモノラル和を、
// 3本以上のときはチャンネルを順に割り当てる(L,R,L,R...)。
func directStage(ctx context.Context, p project.Project, bus [][]float32, sr int, set spatial.Set, total int) ([][]float32, error) {
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
			res[i], errs[i] = spatial.Direct(ctx, feed, sr, d, az, el, set, spatial.DirectParams{
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
func reverbStage(ctx context.Context, bus [][]float32, ir [][]float32, total int) ([][]float32, error) {
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
