package dsp

// 参照実装。チャンク処理(ストリーミング化)の前の、曲全体を一度に処理する版を一字一句そのまま残したもの。
// 流し処理版は、これとビット単位で一致することをテストで確かめる(テスト内だけに残す)。
// 関数名に ref を付けた以外は、元のコードのまま。

import (
	"context"
	"math"
	"runtime"
	"sync"

	"gonum.org/v1/gonum/dsp/fourier"
)

func refConvolve(ctx context.Context, x, ir []float32) ([]float32, error) {
	if len(x) == 0 || len(ir) == 0 {
		return []float32{}, nil
	}
	if len(ir) <= shortIRMax {
		out, err := refConvolveShort(ctx, x, ir)
		if err != nil {
			return nil, err
		}
		return out[0], nil
	}
	return refConvolvePartitioned(ctx, x, ir, partitionSize(len(ir)))
}

func refConvolvePair(ctx context.Context, x, irA, irB []float32) ([]float32, []float32, error) {
	if len(x) > 0 && len(irA) > 0 && len(irB) > 0 && len(irA) <= shortIRMax && len(irB) <= shortIRMax {
		out, err := refConvolveShort(ctx, x, irA, irB)
		if err != nil {
			return nil, nil, err
		}
		return out[0], out[1], nil
	}
	var a, b []float32
	var errA, errB error
	done := make(chan struct{})
	go func() {
		defer close(done)
		b, errB = refConvolve(ctx, x, irB)
	}()
	a, errA = refConvolve(ctx, x, irA)
	<-done
	if errA != nil {
		return nil, nil, errA
	}
	return a, b, errB
}

func refConvolveShort(ctx context.Context, x []float32, irs ...[]float32) ([][]float32, error) {
	L := 0
	for _, ir := range irs {
		L = max(L, len(ir))
	}
	N := shortFFTSize
	B := N - L + 1
	fft := fourier.NewFFT(N)
	nc := N/2 + 1
	buf := make([]float64, N)
	H := make([][]complex128, len(irs))
	outs := make([][]float32, len(irs))
	maxOut := 0
	for k, ir := range irs {
		for i := range buf {
			buf[i] = 0
		}
		for i := range ir {
			buf[i] = float64(ir[i])
		}
		H[k] = fft.Coefficients(make([]complex128, nc), buf)
		outs[k] = make([]float32, len(x)+len(ir)-1)
		maxOut = max(maxOut, len(outs[k]))
	}
	X := make([]complex128, nc)
	Y := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)
	for start, blk := 0, 0; start < maxOut; start, blk = start+B, blk+1 {
		if blk%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// フレームは x[start-(L-1) .. start-(L-1)+N)。先頭の L-1 点は巡回で汚れるので捨てる
		for i := 0; i < N; i++ {
			if j := start - (L - 1) + i; j >= 0 && j < len(x) {
				buf[i] = float64(x[j])
			} else {
				buf[i] = 0
			}
		}
		fft.Coefficients(X, buf)
		for k := range irs {
			for i := range Y {
				Y[i] = X[i] * H[k][i]
			}
			fft.Sequence(y, Y)
			out := outs[k]
			for i := 0; i < B && start+i < len(out); i++ {
				out[start+i] = float32(y[L-1+i] * scale)
			}
		}
	}
	return outs, nil
}

func refConvolvePartitioned(ctx context.Context, x, ir []float32, B int) ([]float32, error) {
	N := 2 * B
	nc := N/2 + 1
	fft := fourier.NewFFT(N)

	// IRをB点ずつに分けてスペクトルにしておく
	parts := (len(ir) + B - 1) / B
	H := make([][]complex128, parts)
	buf := make([]float64, N)
	for p := 0; p < parts; p++ {
		for i := range buf {
			buf[i] = 0
		}
		for i := 0; i < B && p*B+i < len(ir); i++ {
			buf[i] = float64(ir[p*B+i])
		}
		H[p] = fft.Coefficients(make([]complex128, nc), buf)
	}

	outLen := len(x) + len(ir) - 1
	blocks := (outLen + B - 1) / B
	out := make([]float32, outLen)

	// 周波数領域の遅延線(新しい入力スペクトルほど小さい添字)
	fdl := make([][]complex128, parts)
	for i := range fdl {
		fdl[i] = make([]complex128, nc)
	}
	acc := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)

	for k := 0; k < blocks; k++ {
		if k%16 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// 入力フレーム = [前のブロック, 今のブロック]
		for i := 0; i < N; i++ {
			j := (k-1)*B + i
			if j >= 0 && j < len(x) {
				buf[i] = float64(x[j])
			} else {
				buf[i] = 0
			}
		}
		// 最古のスペクトルの配列を再利用して新しいスペクトルを入れる
		oldest := fdl[parts-1]
		copy(fdl[1:], fdl[:parts-1])
		fdl[0] = fft.Coefficients(oldest, buf)

		for i := range acc {
			acc[i] = 0
		}
		for p := 0; p < parts; p++ {
			a, h := fdl[p], H[p]
			for i := 0; i < nc; i++ {
				acc[i] += a[i] * h[i]
			}
		}
		fft.Sequence(y, acc)
		for i := 0; i < B; i++ {
			o := k*B + i
			if o >= outLen {
				break
			}
			out[o] = float32(y[B+i] * scale)
		}
	}
	return out, nil
}

func refCompress(buf [][]float32, sr int, p CompParams) {
	if len(buf) == 0 || p.Ratio <= 1 {
		return
	}
	coef := func(ms float64) float64 {
		return math.Exp(-1 / (math.Max(ms, 0.01) * 1e-3 * float64(sr)))
	}
	att, rel := coef(p.AttackMs), coef(p.ReleaseMs)
	slope := 1 - 1/p.Ratio
	gr := 0.0 // 現在のゲインリダクション(dB, 正)
	n := len(buf[0])
	for i := 0; i < n; i++ {
		peak := 0.0
		for _, ch := range buf {
			peak = math.Max(peak, math.Abs(float64(ch[i])))
		}
		target := math.Max(LinToDb(peak)-p.ThresholdDb, 0) * slope
		if target > gr {
			gr = att*gr + (1-att)*target
		} else {
			gr = rel*gr + (1-rel)*target
		}
		g := float32(DbToLin(-gr))
		for _, ch := range buf {
			ch[i] *= g
		}
	}
}

func refIntegratedLUFS(buf [][]float32, sr int) float64 {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return math.Inf(-1)
	}
	n := len(buf[0])
	seg := sr / 10 // 100ms
	nSeg := (n + seg - 1) / seg
	// チャンネルごとに独立なので並列にフィルタして、セグメントごとの二乗和を求める
	perCh := make([][]float64, len(buf))
	var wg sync.WaitGroup
	for c, ch := range buf {
		wg.Add(1)
		go func() {
			defer wg.Done()
			energy := make([]float64, nSeg)
			shelf, hp := kWeighting(float64(sr))
			for s := 0; s < nSeg; s++ {
				end := min((s+1)*seg, n)
				sum := 0.0
				for i := s * seg; i < end; i++ {
					y := hp.ProcessSample(shelf.ProcessSample(float64(ch[i])))
					sum += y * y
				}
				energy[s] = sum
			}
			perCh[c] = energy
		}()
	}
	wg.Wait()
	segEnergy := make([]float64, nSeg) // 各100msセグメントの二乗和(全チャンネル合計)
	for _, e := range perCh {
		for s, v := range e {
			segEnergy[s] += v
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

func refTruePeakLimit(buf [][]float32, sr int, ceilingDb float64) {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return
	}
	n := len(buf[0])
	ceil := DbToLin(ceilingDb)

	need := refComputeNeed(buf, ceil, true)

	look := max(int(limiterLookaheadMs*1e-3*float64(sr)), 1)
	gmin := refSlidingMin(need, look)

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

func refSlidingMin(x []float32, w int) []float32 {
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

func refComputeNeed(buf [][]float32, ceil float64, guard bool) []float32 {
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
				if guard && refLocalMax(buf, b-guardSpan, end+guardSpan)*interpGain <= ceil {
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

func refLocalMax(buf [][]float32, from, to int) float64 {
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

func refLR4LowPass(x []float32, fs, f0 float64) {
	LowPass(fs, f0).Process(x)
	LowPass(fs, f0).Process(x)
}

func refLR4HighPass(x []float32, fs, f0 float64) {
	HighPass(fs, f0).Process(x)
	HighPass(fs, f0).Process(x)
}
