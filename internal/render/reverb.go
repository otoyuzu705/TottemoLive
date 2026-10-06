package render

import (
	"context"
	"runtime"
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
	tmp    [3][]float32 // 左の高域・右の高域・合計の低域(別々の配列で並列に計算する)
}

// radiatedParallelMinFrames は、radiatedProc.Process が3本のフィルタを並列に処理し始めるフレーム数。
const radiatedParallelMinFrames = 4096

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
//
// サブ有効時の LR4 3本(左の高域・右の高域・左右の合計の低域)は、別々の配列で計算するので並列に回す
// (3本は互いに独立)。足し算は、元と同じ 左/2 → 右/2 → 低域×g の順に固定する。
func (r *radiatedProc) Process(bus [][]float32) []float32 {
	n := len(bus[0])
	if cap(r.mono) < n {
		r.mono = make([]float32, n)
		for k := range r.tmp {
			r.tmp[k] = make([]float32, n)
		}
	}
	mono := r.mono[:n]
	if !r.active {
		for i := range mono {
			mono[i] = (bus[0][i] + bus[1][i]) / 2
		}
		return mono
	}
	t0, t1, t2 := r.tmp[0][:n], r.tmp[1][:n], r.tmp[2][:n]
	hpL := func() { copy(t0, bus[0]); r.hp[0].Process(t0) }
	hpR := func() { copy(t1, bus[1]); r.hp[1].Process(t1) }
	lpSum := func() {
		for i := range t2 {
			t2[i] = bus[0][i] + bus[1][i]
		}
		r.lp.Process(t2)
	}
	if n >= radiatedParallelMinFrames && runtime.GOMAXPROCS(0) > 1 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); hpR() }()
		go func() { defer wg.Done(); lpSum() }()
		hpL()
		wg.Wait()
	} else {
		hpL()
		hpR()
		lpSum()
	}
	clear(mono)
	for i := range mono {
		mono[i] += t0[i] / 2
		mono[i] += t1[i] / 2
		mono[i] += t2[i] * r.g
	}
	return mono
}

// radiatedMono は、バス全体の放射された音のモノラル表現を返す。radiatedProc に全体を渡す包み。
func radiatedMono(bus [][]float32, active bool, sub project.Sub) []float32 {
	return append([]float32(nil), newRadiatedProc(active, sub).Process(bus)...)
}

// reverbProc はスピーカーから放射された音(radiatedProc)を会場IR(左右)で畳み込む処理器。
// 残響は距離減衰を掛ける前の信号で駆動する(拡散音場のレベルは距離に依らないため)。
// 左右のIRは長さ・分割サイズが同じなので、1つの畳み込み器(周波数領域遅延線を共有、ブロックは並列)で処理する。
type reverbProc struct {
	rad  *radiatedProc
	conv *dsp.StreamConvolver
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
		conv: dsp.NewStreamConvolver(ir[0], ir[1]),
		out:  newFrameQueue(2),
	}
}

// Push はバスの続き(ステレオ)を与える。バスは Push の間だけ読む(返った後は触らない)。
// 呼び出し側(パス1)は、次のチャンクの先読みを別の面に書いて Push と重ねるので、この不変条件が前提になる。
func (r *reverbProc) Push(bus [][]float32) {
	mono := r.rad.Process(bus)
	r.out.push(r.conv.Process(mono))
}

// Flush は入力の終わりを知らせ、残響の尾を出す。
func (r *reverbProc) Flush() {
	r.out.push(r.conv.Flush())
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
