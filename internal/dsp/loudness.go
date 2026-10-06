package dsp

import (
	"math"
	"sync"
)

// kWeighting は ITU-R BS.1770 のKウェイトフィルタ(高域シェルフ + 高域通過)を任意のサンプルレートで作る。
func kWeighting(fs float64) (*Biquad, *Biquad) {
	// 段1: 頭部の影響を模す高域シェルフ
	f0, G, Q := 1681.974450955533, 3.999843853973347, 0.7071752369554196
	K := math.Tan(math.Pi * f0 / fs)
	Vh := math.Pow(10, G/20)
	Vb := math.Pow(Vh, 0.4996667741545416)
	a0 := 1 + K/Q + K*K
	shelf := &Biquad{
		b0: (Vh + Vb*K/Q + K*K) / a0,
		b1: 2 * (K*K - Vh) / a0,
		b2: (Vh - Vb*K/Q + K*K) / a0,
		a1: 2 * (K*K - 1) / a0,
		a2: (1 - K/Q + K*K) / a0,
	}
	// 段2: RLB(高域通過)
	f0, Q = 38.13547087602444, 0.5003270373238773
	K = math.Tan(math.Pi * f0 / fs)
	a0 = 1 + K/Q + K*K
	hp := &Biquad{
		b0: 1, b1: -2, b2: 1,
		a1: 2 * (K*K - 1) / a0,
		a2: (1 - K/Q + K*K) / a0,
	}
	return shelf, hp
}

// LoudnessMeter はBS.1770-4のゲート付き統合ラウドネスを、信号を区切って順に Write しながら測る。
// 持つ状態は、Kウェイトフィルタの状態、途中の100msセグメントの二乗和、完了したセグメントのエネルギー列
// (相対ゲートは全ブロックを見て決まるので、エネルギー列は最後まで持つ。60分の曲で 36000 個 × 8 バイト)。
// 結果は、区切り方に依らず、全体を一度に測った場合とビット単位で一致する。
type LoudnessMeter struct {
	sr, seg  int
	n        int          // 書き込んだフレーム数
	filters  [][2]*Biquad // チャンネルごとの [shelf, hp]
	sum      []float64    // チャンネルごとの、途中のセグメントの二乗和
	inSeg    int          // 途中のセグメントに入ったフレーム数
	segEnerg []float64    // 完了したセグメントのエネルギー(全チャンネル合計)
}

// NewLoudnessMeter は channels チャンネルの信号を測るメーターを返す。
func NewLoudnessMeter(sr, channels int) *LoudnessMeter {
	m := &LoudnessMeter{sr: sr, seg: sr / 10, filters: make([][2]*Biquad, channels), sum: make([]float64, channels)}
	for c := range m.filters {
		shelf, hp := kWeighting(float64(sr))
		m.filters[c] = [2]*Biquad{shelf, hp}
	}
	return m
}

// parallelMinFrames は、チャンネルを並列に処理し始める Write の長さ(フレーム数)。
const parallelMinFrames = 8192

// Write は buf(チャンネル別、全チャンネル同じ長さ)を続きの信号として測る。
func (m *LoudnessMeter) Write(buf [][]float32) {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return
	}
	n := len(buf[0])
	done := make([][]float64, len(buf)) // チャンネルごとの、この Write で完了したセグメントのエネルギー
	process := func(c int) {
		shelf, hp := m.filters[c][0], m.filters[c][1]
		sum, inSeg := m.sum[c], m.inSeg
		for _, v := range buf[c] {
			y := hp.ProcessSample(shelf.ProcessSample(float64(v)))
			sum += y * y
			if inSeg++; inSeg == m.seg {
				done[c] = append(done[c], sum)
				sum, inSeg = 0, 0
			}
		}
		m.sum[c] = sum
	}
	if n >= parallelMinFrames && len(buf) > 1 {
		var wg sync.WaitGroup
		for c := range buf {
			wg.Add(1)
			go func() {
				defer wg.Done()
				process(c)
			}()
		}
		wg.Wait()
	} else {
		for c := range buf {
			process(c)
		}
	}
	// 完了したセグメントを、全チャンネルで合計する(0 + ch0 + ch1 の順)
	for s := range done[0] {
		e := 0.0
		for c := range done {
			e += done[c][s]
		}
		m.segEnerg = append(m.segEnerg, e)
	}
	m.inSeg = (m.inSeg + n) % m.seg
	m.n += n
}

// Integrated はここまでに Write した信号全体の統合ラウドネス(LUFS)を返す。何度呼んでもよい。
// ブロックが作れない短い信号は全体の平均で測る。無音・空は -Inf。
func (m *LoudnessMeter) Integrated() float64 {
	if m.n == 0 {
		return math.Inf(-1)
	}
	n, seg := m.n, m.seg
	segEnergy := m.segEnerg
	if m.inSeg > 0 { // 途中のセグメントも、最後の(短い)セグメントとして数える
		e := 0.0
		for _, v := range m.sum {
			e += v
		}
		segEnergy = append(segEnergy[:len(segEnergy):len(segEnergy)], e)
	}
	nSeg := len(segEnergy)
	lufs := func(power float64) float64 { return -0.691 + 10*math.Log10(power) }

	const segPerBlock = 4
	if nSeg < segPerBlock || n < segPerBlock*seg {
		total := 0.0
		for _, e := range segEnergy {
			total += e
		}
		return lufs(total / float64(n)) // チャンネル合計の二乗和 / サンプル数
	}
	nBlocks := nSeg - segPerBlock + 1
	blockPow := make([]float64, 0, nBlocks)
	for b := 0; b < nBlocks; b++ {
		e, cnt := 0.0, 0
		for s := b; s < b+segPerBlock; s++ {
			e += segEnergy[s]
			cnt += min((s+1)*seg, n) - s*seg
		}
		blockPow = append(blockPow, e/float64(cnt))
	}

	mean := func(thresh float64) (float64, int) {
		sum, c := 0.0, 0
		for _, p := range blockPow {
			if lufs(p) > thresh {
				sum += p
				c++
			}
		}
		if c == 0 {
			return 0, 0
		}
		return sum / float64(c), c
	}
	absMean, c := mean(-70)
	if c == 0 {
		return math.Inf(-1)
	}
	relMean, c := mean(lufs(absMean) - 10)
	if c == 0 {
		return lufs(absMean)
	}
	return lufs(relMean)
}

// IntegratedLUFS はBS.1770-4のゲート付き統合ラウドネス(400msブロック、75%オーバーラップ、
// 絶対-70LUFS・相対-10LU)を返す。チャンネル重みは全て1(ステレオ)。
// ブロックが作れない短い信号は全体の平均で測る。無音は -Inf。
func IntegratedLUFS(buf [][]float32, sr int) float64 {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return math.Inf(-1)
	}
	m := NewLoudnessMeter(sr, len(buf))
	m.Write(buf)
	return m.Integrated()
}
