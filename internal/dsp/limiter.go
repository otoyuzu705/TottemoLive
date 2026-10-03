package dsp

import (
	"math"
	"runtime"
	"sync"
)

// リミッタの処理方式の定数。ユーザーが調整するのはピーク上限(output.ceilingDbTp)だけ。
const (
	limiterLookaheadMs = 1.5
	limiterReleaseMs   = 50
	// 粗い推定(4倍・位相あたり16タップ)で全サンプルを見て、上限の refineFraction 以上のサンプルだけ、
	// 精密な推定(8倍・位相あたり48タップ)でも確かめる。粗い推定は帯域いっぱいのノイズで最大 約2 dB 低く見積もるので、
	// refineFraction(0.6 = -4.4 dB)はそれを見込んだ余裕。精密な推定は約3.7倍重いが、対象は大きなサンプルだけ
	coarseOversample = 4
	coarseTaps       = 16
	fineOversample   = 8
	fineTaps         = 48
	refineFraction   = 0.6
)

// firTable は補間用の位相別FIR(窓付きsinc)。[位相][タップ]。位相0は元サンプル(使わない)。
type firTable struct {
	over, taps int
	h          [][]float64
}

func newFIRTable(over, taps int) *firTable {
	t := &firTable{over: over, taps: taps, h: make([][]float64, over)}
	for ph := 0; ph < over; ph++ {
		t.h[ph] = make([]float64, taps)
		frac := float64(ph) / float64(over)
		for k := 0; k < taps; k++ {
			// タップ k は サンプル n+k-(taps/2-1) に掛かる。補間点は n+frac。
			x := float64(k-(taps/2-1)) - frac
			s := 1.0
			if x != 0 {
				s = math.Sin(math.Pi*x) / (math.Pi * x)
			}
			w := 0.5 + 0.5*math.Cos(math.Pi*x/float64(taps/2))
			t.h[ph][k] = s * w
		}
	}
	return t
}

var (
	coarseFIR = newFIRTable(coarseOversample, coarseTaps)
	fineFIR   = newFIRTable(fineOversample, fineTaps)
)

// peak はサンプル n の周辺のトゥルーピーク推定(元サンプルと、次のサンプルとの間の補間値の最大絶対値)。
func (t *firTable) peak(x []float32, n int) float64 {
	peak := math.Abs(float64(x[n]))
	for ph := 1; ph < t.over; ph++ {
		sum := 0.0
		for k, h := range t.h[ph] {
			j := n + k - (t.taps/2 - 1)
			if j >= 0 && j < len(x) {
				sum += float64(x[j]) * h
			}
		}
		peak = math.Max(peak, math.Abs(sum))
	}
	return peak
}

// gain は、補間値の絶対値が近傍のサンプル最大値の何倍までありうるかの上限(位相ごとの係数の絶対値の和の最大)。
func (t *firTable) gain() float64 {
	g := 1.0
	for ph := 1; ph < t.over; ph++ {
		sum := 0.0
		for _, v := range t.h[ph] {
			sum += math.Abs(v)
		}
		g = math.Max(g, sum)
	}
	return g
}

// TruePeakLimit はステレオリンクのルックアヘッド・リミッタ。オーバーサンプルで推定した
// トゥルーピークが ceilingDb(dBTP)を超えないようにゲインを下げる(その場で処理)。
// 必要ゲインの窓内最小値を取り、対称移動平均でなめらかにしたうえで、リリースを掛ける。
// 移動平均の窓が最小値フィルタの窓以下なので、ピーク位置のゲインは必要ゲイン以下になる。
func TruePeakLimit(buf [][]float32, sr int, ceilingDb float64) {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return
	}
	n := len(buf[0])
	ceil := DbToLin(ceilingDb)

	need := computeNeed(buf, ceil, true)

	look := max(int(limiterLookaheadMs*1e-3*float64(sr)), 1)
	gmin := slidingMin(need, look)

	// 対称移動平均(窓 2*look+1)
	prefix := make([]float64, n+1)
	for i, v := range gmin {
		prefix[i+1] = prefix[i] + float64(v)
	}
	relCoef := 1 - math.Exp(-1/(limiterReleaseMs*1e-3*float64(sr)))
	r := 1.0
	for i := 0; i < n; i++ {
		lo, hi := max(i-look, 0), min(i+look, n-1)
		// 範囲外は、端の値(gmin[0] / gmin[n-1])で埋めて平均する。1(ゲイン変化なし)で埋めると、
		// 信号の先頭・末尾の look サンプル以内にあるピークで、ゲインが必要ゲインより高くなり上限を超える
		sum := prefix[hi+1] - prefix[lo] +
			float64(max(look-i, 0))*float64(gmin[0]) +
			float64(max(i+look-(n-1), 0))*float64(gmin[n-1])
		g := sum / float64(2*look+1)
		// リリース: gに追従して下がり、上がるときはゆっくり戻る。常に r <= g
		r = math.Min(g, r+(1-r)*relCoef)
		gf := float32(r)
		for _, ch := range buf {
			ch[i] *= gf
		}
	}
}

// slidingMin は x の各位置を中心とした窓 [i-w, i+w] の最小値を返す(単調デックでO(n))。
func slidingMin(x []float32, w int) []float32 {
	n := len(x)
	out := make([]float32, n)
	dq := make([]int, 0, 2*w+2)
	head := 0
	next := 0 // 次にデックへ入れる添字
	for i := 0; i < n; i++ {
		for ; next <= min(i+w, n-1); next++ {
			for len(dq) > head && x[dq[len(dq)-1]] >= x[next] {
				dq = dq[:len(dq)-1]
			}
			dq = append(dq, next)
		}
		for dq[head] < i-w {
			head++
		}
		out[i] = x[dq[head]]
		if head > 4096 { // デックの先頭を詰めてメモリを抑える
			dq = append(dq[:0], dq[head:]...)
			head = 0
		}
	}
	return out
}

// guardBlock は、トゥルーピークの計算を省けるか判定する単位(サンプル数)。
const guardBlock = 32

// interpGain は、いずれの推定でも補間値の絶対値が近傍のサンプル最大値の何倍までかの上限。
var interpGain = math.Max(coarseFIR.gain(), fineFIR.gain())

// guardSpan は、補間値が影響を受ける近傍の広さ(片側のサンプル数)。精密な推定のタップ数の半分。
const guardSpan = fineTaps / 2

// computeNeed は各サンプルで必要なゲイン(上限を超えなければ1)を返す。各サンプルが独立なので区間に分けて並列に求める。
// guard が true のときは、近傍(±guardSpan サンプル)の最大値 × interpGain が上限以下の
// ブロックでは、補間値も上限以下だと分かるのでトゥルーピークの計算を省く。結果は変わらない。
func computeNeed(buf [][]float32, ceil float64, guard bool) []float32 {
	n := len(buf[0])
	need := make([]float32, n)
	workers := runtime.GOMAXPROCS(0)
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := lo; b < hi; b += guardBlock {
				end := min(b+guardBlock, hi)
				if guard && localMax(buf, b-guardSpan, end+guardSpan)*interpGain <= ceil {
					for i := b; i < end; i++ {
						need[i] = 1
					}
					continue
				}
				for i := b; i < end; i++ {
					p := 0.0
					for _, ch := range buf {
						pc := coarseFIR.peak(ch, i)
						if pc >= ceil*refineFraction {
							pc = math.Max(pc, fineFIR.peak(ch, i)) // 大きいサンプルだけ精密に確かめる
						}
						p = math.Max(p, pc)
					}
					if p > ceil {
						need[i] = float32(ceil / p)
					} else {
						need[i] = 1
					}
				}
			}
		}()
	}
	wg.Wait()
	return need
}

// localMax は [from, to) の範囲(信号の外は無視)の最大絶対値(全チャンネル)。
func localMax(buf [][]float32, from, to int) float64 {
	from, to = max(from, 0), min(to, len(buf[0]))
	m := float32(0)
	for _, ch := range buf {
		for _, v := range ch[from:to] {
			if v < 0 {
				v = -v
			}
			if v > m {
				m = v
			}
		}
	}
	return float64(m)
}
