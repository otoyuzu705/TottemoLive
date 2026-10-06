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

// peakAt は peak と同じ計算を、信号の一部(絶対位置 base から始まる x。信号全体の長さは total)に対して行う。
func (t *firTable) peakAt(x []float32, base, total, n int) float64 {
	peak := math.Abs(float64(x[n-base]))
	for ph := 1; ph < t.over; ph++ {
		sum := 0.0
		for k, h := range t.h[ph] {
			j := n + k - (t.taps/2 - 1)
			if j >= 0 && j < total {
				sum += float64(x[j-base]) * h
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

// guardBlock は、トゥルーピークの計算を省けるか判定する単位(サンプル数)。
const guardBlock = 32

// interpGain は、いずれの推定でも補間値の絶対値が近傍のサンプル最大値の何倍までかの上限。
var interpGain = math.Max(coarseFIR.gain(), fineFIR.gain())

// guardSpan は、補間値が影響を受ける近傍の広さ(片側のサンプル数)。精密な推定のタップ数の半分。
const guardSpan = fineTaps / 2

// parallelNeedMin は、必要ゲインの計算を並列に分け始める区間の長さ(サンプル数)。
const parallelNeedMin = 8192

// computeNeedRange は区間 [from, to) の各サンプルで必要なゲイン(上限を超えなければ1)を返す。
// x は信号の一部(チャンネル別、絶対位置 base から始まる)で、信号全体の長さ(これまでに受け取った数)が total。
// 区間の近傍(±guardSpan サンプル。total の外は無視)が x に含まれていること。
// 各サンプルが独立なので区間に分けて並列に求める。
// guard が true のときは、近傍の最大値 × interpGain が上限以下のブロックでは、補間値も上限以下だと分かるので
// トゥルーピークの計算を省く。結果は変わらない。
func computeNeedRange(x [][]float32, base, total, from, to int, ceil float64, guard bool) []float32 {
	cnt := to - from
	need := make([]float32, max(cnt, 0))
	if cnt <= 0 {
		return need
	}
	workers := 1
	if cnt >= parallelNeedMin {
		workers = runtime.GOMAXPROCS(0)
	}
	chunk := (cnt + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := from; lo < to; lo += chunk {
		hi := min(lo+chunk, to)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := lo; b < hi; b += guardBlock {
				end := min(b+guardBlock, hi)
				if guard && localMaxAt(x, base, total, b-guardSpan, end+guardSpan)*interpGain <= ceil {
					for i := b; i < end; i++ {
						need[i-from] = 1
					}
					continue
				}
				for i := b; i < end; i++ {
					p := 0.0
					for _, ch := range x {
						pc := coarseFIR.peakAt(ch, base, total, i)
						if pc >= ceil*refineFraction {
							pc = math.Max(pc, fineFIR.peakAt(ch, base, total, i)) // 大きいサンプルだけ精密に確かめる
						}
						p = math.Max(p, pc)
					}
					if p > ceil {
						need[i-from] = float32(ceil / p)
					} else {
						need[i-from] = 1
					}
				}
			}
		}()
	}
	wg.Wait()
	return need
}

// localMaxAt は [from, to) の範囲(信号 [0, total) の外は無視)の最大絶対値(全チャンネル)。x は絶対位置 base から。
func localMaxAt(x [][]float32, base, total, from, to int) float64 {
	from, to = max(from, 0), min(to, total)
	m := float32(0)
	for _, ch := range x {
		for _, v := range ch[from-base : max(to, from)-base] {
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

// Limiter はステレオリンクのルックアヘッド・リミッタ。オーバーサンプルで推定した
// トゥルーピークが ceilingDb(dBTP)を超えないようにゲインを下げる。
// 必要ゲインの窓内最小値を取り、対称移動平均でなめらかにしたうえで、リリースを掛ける。
// 移動平均の窓が最小値フィルタの窓以下なので、ピーク位置のゲインは必要ゲイン以下になる。
//
// 信号を区切って順に Process し、最後に Flush する。出力は 2*look+guardSpan サンプル遅れて出てくる
// (サンプル数は信号の長さと同じ)。結果は、信号全体を一度に処理した場合と、区切り方に依らずビット単位で一致する。
// 信号の端の扱い(トゥルーピーク推定のFIRの外は0、移動平均の外は端の値で埋める)も同じで、
// 端が確定する(Flush)まで、端の値を使う出力は作らない。
type Limiter struct {
	look    int
	ceil    float64
	relCoef float64
	r       float64 // リリースの状態(現在のゲイン)

	total  int // 受け取ったフレーム数
	needN  int // 必要ゲインを求めた数(区間 [0, needN))
	gminN  int // 最小値フィルタを通した数
	outN   int // 出力した数
	finish bool

	x       [][]float32 // 入力(元の値)。絶対位置 xBase から
	xBase   int
	need    []float32 // 必要ゲイン。絶対位置 needBase から
	needBas int
	dq      []int // 最小値フィルタの単調デック(絶対位置)
	dqHead  int
	dqNext  int // 次にデックへ入れる添字
	prefix  []float64
	pBase   int // prefix[k] は絶対位置 pBase + k(prefix[k] = Σ_{i<k} gmin[i])
	gmin0   float32
	gminEnd float32 // 最後に求めた gmin
	out     [][]float32
}

// NewLimiter は channels チャンネルのリミッタを返す。
func NewLimiter(sr, channels int, ceilingDb float64) *Limiter {
	return &Limiter{
		look:    max(int(limiterLookaheadMs*1e-3*float64(sr)), 1),
		ceil:    DbToLin(ceilingDb),
		relCoef: 1 - math.Exp(-1/(limiterReleaseMs*1e-3*float64(sr))),
		r:       1.0,
		x:       make([][]float32, channels),
		prefix:  []float64{0},
		out:     make([][]float32, channels),
	}
}

// Process は信号の続き in を与え(複製して受け取る)、出力できるようになったフレームを返す。
// 戻り値は次の Process / Flush まで有効。
func (l *Limiter) Process(in [][]float32) [][]float32 {
	if len(in) == 0 {
		return l.out
	}
	for c := range l.x {
		l.x[c] = append(l.x[c], in[c]...)
	}
	l.total += len(in[0])
	return l.advance()
}

// Flush は信号の終わりを知らせ、残りの出力を返す。戻り値は次の呼び出しまで有効。
func (l *Limiter) Flush() [][]float32 {
	l.finish = true
	return l.advance()
}

func (l *Limiter) advance() [][]float32 {
	for c := range l.out {
		l.out[c] = l.out[c][:0]
	}
	if l.total == 0 {
		return l.out
	}
	look, n := l.look, l.total

	// 1. 必要ゲイン。近傍(後ろ guardSpan サンプル)が揃った所まで(終わりが確定していれば全部)
	hi := n - guardSpan
	if l.finish {
		hi = n
	}
	if hi > l.needN {
		l.need = append(l.need, computeNeedRange(l.x, l.xBase, n, l.needN, hi, l.ceil, true)...)
		l.needN = hi
	}

	// 2. 窓内の最小値 [i-look, i+look](終わりの外は切り詰め)。窓の後ろ側が揃った所まで
	for i := l.gminN; i+look < l.needN || (l.finish && l.needN == n && i < n); i++ {
		for ; l.dqNext <= min(i+look, l.needN-1); l.dqNext++ {
			v := l.need[l.dqNext-l.needBas]
			for len(l.dq) > l.dqHead && l.need[l.dq[len(l.dq)-1]-l.needBas] >= v {
				l.dq = l.dq[:len(l.dq)-1]
			}
			l.dq = append(l.dq, l.dqNext)
		}
		for l.dq[l.dqHead] < i-look {
			l.dqHead++
		}
		g := l.need[l.dq[l.dqHead]-l.needBas]
		if i == 0 {
			l.gmin0 = g
		}
		l.gminEnd = g
		l.prefix = append(l.prefix, l.prefix[len(l.prefix)-1]+float64(g))
		l.gminN = i + 1
	}

	// 3. 対称移動平均 → リリース → 掛ける。終わりの値を使う出力(i+look > n-1)は、終わりが確定してから
	done := l.finish && l.gminN == n
	for i := l.outN; i+look < l.gminN || (done && i < n); i++ {
		lo, hi := max(i-look, 0), min(i+look, n-1)
		// 範囲外は、端の値(gmin[0] / gmin[n-1])で埋めて平均する(信号の途中では、終わりの外には届かない)
		sum := l.prefix[hi+1-l.pBase] - l.prefix[lo-l.pBase] +
			float64(max(look-i, 0))*float64(l.gmin0)
		if done {
			sum += float64(max(i+look-(n-1), 0)) * float64(l.gminEnd)
		}
		g := sum / float64(2*look+1)
		// リリース: gに追従して下がり、上がるときはゆっくり戻る。常に r <= g
		l.r = math.Min(g, l.r+(1-l.r)*l.relCoef)
		gf := float32(l.r)
		for c := range l.out {
			l.out[c] = append(l.out[c], l.x[c][i-l.xBase]*gf)
		}
		l.outN = i + 1
	}

	l.trim()
	return l.out
}

// trim は、もう使わない過去のデータを捨てる。
func (l *Limiter) trim() {
	// 入力: 出力がこれから使う位置と、必要ゲインの計算がこれから読む位置(近傍の前側)のうち、早いほうから
	keep := max(min(l.outN, l.needN-guardSpan), 0)
	if d := keep - l.xBase; d > 0 {
		for c := range l.x {
			l.x[c] = l.x[c][:copy(l.x[c], l.x[c][d:])]
		}
		l.xBase = keep
	}
	// 必要ゲイン: 最小値フィルタがこれから読む位置から。デックの中には、直前の窓の前端(gminN-1-look)の
	// 値がまだ残っていて、次の入力と比べるために読まれる
	keepNeed := max(l.gminN-1-l.look, 0)
	if d := keepNeed - l.needBas; d > 0 {
		l.need = l.need[:copy(l.need, l.need[d:])]
		l.needBas = keepNeed
	}
	if l.dqHead > 4096 { // デックの先頭を詰めてメモリを抑える
		l.dq = append(l.dq[:0], l.dq[l.dqHead:]...)
		l.dqHead = 0
	}
	// 累積和: 移動平均がこれから読む位置(窓の前側)から
	keepP := max(l.outN-l.look, 0)
	if d := keepP - l.pBase; d > 0 {
		l.prefix = l.prefix[:copy(l.prefix, l.prefix[d:])]
		l.pBase = keepP
	}
}

// TruePeakLimit は buf 全体にリミッタを掛ける(その場で処理)。Limiter に全体を流す包み。
func TruePeakLimit(buf [][]float32, sr int, ceilingDb float64) {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return
	}
	l := NewLimiter(sr, len(buf), ceilingDb)
	pos := 0
	put := func(out [][]float32) {
		for c := range buf {
			copy(buf[c][pos:], out[c])
		}
		pos += len(out[0])
	}
	put(l.Process(buf))
	put(l.Flush())
}
