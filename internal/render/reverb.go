package render

import (
	"context"
	"sync"

	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

// radiatedProc は、スピーカーから放射された音のモノラル表現(会場の残響を励起する音)を作る処理器。
// サブが無効なら バスの左右平均。有効なら メインの高域(クロスオーバーより上)の左右平均 +
// サブの低域(左右の合計のクロスオーバーより下に、サブのレベルを掛けたものの半分)。
// 半分にするのは、サブ経路のモノ合計(左右の和)をメイン2本が出す低域(左右平均)の大きさにそろえるため:
// サブ 0 dB で、分けない場合(左右平均)と同じ大きさになり、サブのレベルを上げると残響の低域もその分増える。
// 実際の会場では、サブが強く鳴るほど部屋の低域の残響も増える。
type radiatedProc struct {
	active bool
	hp     [2]*dsp.LR4
	lp     *dsp.LR4
	g      float32
	mono   []float32
	tmp    []float32
}

func newRadiatedProc(active bool, sub project.Sub) *radiatedProc {
	r := &radiatedProc{active: active}
	if active {
		fs := float64(sampleRate)
		r.hp = [2]*dsp.LR4{dsp.NewLR4HighPass(fs, sub.CrossoverHz), dsp.NewLR4HighPass(fs, sub.CrossoverHz)}
		r.lp = dsp.NewLR4LowPass(fs, sub.CrossoverHz)
		r.g = float32(dsp.DbToLin(sub.LevelDb) / 2)
	}
	return r
}

// Process はバスの続きから、放射された音のモノラル表現を返す(戻り値は次の呼び出しまで有効)。
func (r *radiatedProc) Process(bus [][]float32) []float32 {
	n := len(bus[0])
	if cap(r.mono) < n {
		r.mono = make([]float32, n)
		r.tmp = make([]float32, n)
	}
	mono, tmp := r.mono[:n], r.tmp[:n]
	if !r.active {
		for i := range mono {
			mono[i] = (bus[0][i] + bus[1][i]) / 2
		}
		return mono
	}
	clear(mono)
	for c := 0; c < 2; c++ {
		copy(tmp, bus[c])
		r.hp[c].Process(tmp)
		for i, v := range tmp {
			mono[i] += v / 2
		}
	}
	for i := range tmp {
		tmp[i] = bus[0][i] + bus[1][i]
	}
	r.lp.Process(tmp)
	for i, v := range tmp {
		mono[i] += v * r.g
	}
	return mono
}

// radiatedMono は、バス全体の放射された音のモノラル表現を返す。radiatedProc に全体を渡す包み。
func radiatedMono(bus [][]float32, active bool, sub project.Sub) []float32 {
	return append([]float32(nil), newRadiatedProc(active, sub).Process(bus)...)
}

// reverbProc はスピーカーから放射された音(radiatedProc)を会場IR(左右)で畳み込む処理器。
// 残響は距離減衰を掛ける前の信号で駆動する(拡散音場のレベルは距離に依らないため)。
type reverbProc struct {
	rad  *radiatedProc
	conv [2]*dsp.StreamConvolver
	out  *frameQueue
}

func newReverbProc(pp *prepared, ir [][]float32) *reverbProc {
	sub := pp.p.Sub
	active := subsActive(pp.p)
	if !active {
		sub = project.Sub{} // サブが無効なら、サブの設定は残響に影響しない
	}
	return &reverbProc{
		rad:  newRadiatedProc(active, sub),
		conv: [2]*dsp.StreamConvolver{dsp.NewStreamConvolver(ir[0]), dsp.NewStreamConvolver(ir[1])},
		out:  newFrameQueue(2),
	}
}

// Push はバスの続き(ステレオ)を与える。
func (r *reverbProc) Push(bus [][]float32) {
	mono := r.rad.Process(bus)
	var y [2][]float32
	var wg sync.WaitGroup
	for c := range r.conv {
		wg.Add(1)
		go func() {
			defer wg.Done()
			y[c] = r.conv[c].Process(mono)[0]
		}()
	}
	wg.Wait()
	r.out.push([][]float32{y[0], y[1]})
}

// Flush は入力の終わりを知らせ、残響の尾を出す。
func (r *reverbProc) Flush() {
	var y [2][]float32
	var wg sync.WaitGroup
	for c := range r.conv {
		wg.Add(1)
		go func() {
			defer wg.Done()
			y[c] = r.conv[c].Flush()[0]
		}()
	}
	wg.Wait()
	r.out.push([][]float32{y[0], y[1]})
	r.out.end()
}

// reverbCompute は、バス in から残響を計算する(会場IR ir で畳み込む)。長さは total。
// 先行プレビューは、曲の一部を切り出したバスに対して同じ計算を呼ぶ。処理器 reverbProc に流す包み。
func reverbCompute(ctx context.Context, pp *prepared, in, ir [][]float32, total int) ([][]float32, error) {
	r := newReverbProc(pp, ir)
	for from := 0; from < len(in[0]); from += streamBlock {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(from+streamBlock, len(in[0]))
		r.Push([][]float32{in[0][from:end], in[1][from:end]})
	}
	r.Flush()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.out.collect(total), nil
}
