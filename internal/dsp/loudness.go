package dsp

import "math"

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

// IntegratedLUFS はBS.1770-4のゲート付き統合ラウドネス(400msブロック、75%オーバーラップ、
// 絶対-70LUFS・相対-10LU)を返す。チャンネル重みは全て1(ステレオ)。
// ブロックが作れない短い信号は全体の平均で測る。無音は -Inf。
func IntegratedLUFS(buf [][]float32, sr int) float64 {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return math.Inf(-1)
	}
	n := len(buf[0])
	seg := sr / 10 // 100ms
	nSeg := (n + seg - 1) / seg
	segEnergy := make([]float64, nSeg) // 各100msセグメントの二乗和(全チャンネル合計)
	for _, ch := range buf {
		shelf, hp := kWeighting(float64(sr))
		for s := 0; s < nSeg; s++ {
			end := min((s+1)*seg, n)
			sum := 0.0
			for i := s * seg; i < end; i++ {
				y := hp.ProcessSample(shelf.ProcessSample(float64(ch[i])))
				sum += y * y
			}
			segEnergy[s] += sum
		}
	}
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
