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
	truePeakOversample = 4
	truePeakTaps       = 16 // 位相ごとのFIRタップ数
)

// truePeakFIR は4倍オーバーサンプル用の位相別FIR(窓付きsinc)。[位相][タップ]。位相0は元サンプル。
var truePeakFIR = func() [truePeakOversample][truePeakTaps]float64 {
	var h [truePeakOversample][truePeakTaps]float64
	for ph := 0; ph < truePeakOversample; ph++ {
		frac := float64(ph) / truePeakOversample
		for k := 0; k < truePeakTaps; k++ {
			// タップ k は サンプル n+k-(truePeakTaps/2-1) に掛かる。補間点は n+frac。
			t := float64(k-(truePeakTaps/2-1)) - frac
			s := 1.0
			if t != 0 {
				s = math.Sin(math.Pi*t) / (math.Pi * t)
			}
			w := 0.5 + 0.5*math.Cos(math.Pi*t/(truePeakTaps/2))
			h[ph][k] = s * w
		}
	}
	return h
}()

// truePeak はサンプル n の周辺のトゥルーピーク推定(元サンプルと、次サンプルとの間の補間値の最大絶対値)。
func truePeak(x []float32, n int) float64 {
	peak := math.Abs(float64(x[n]))
	for ph := 1; ph < truePeakOversample; ph++ {
		sum := 0.0
		for k := 0; k < truePeakTaps; k++ {
			j := n + k - (truePeakTaps/2 - 1)
			if j >= 0 && j < len(x) {
				sum += float64(x[j]) * truePeakFIR[ph][k]
			}
		}
		peak = math.Max(peak, math.Abs(sum))
	}
	return peak
}

// TruePeakLimit はステレオリンクのルックアヘッド・リミッタ。4倍オーバーサンプルで推定した
// トゥルーピークが ceilingDb(dBTP)を超えないようにゲインを下げる(その場で処理)。
// 必要ゲインの窓内最小値を取り、対称移動平均でなめらかにしたうえで、リリースを掛ける。
// 移動平均の窓が最小値フィルタの窓以下なので、ピーク位置のゲインは必要ゲイン以下になる。
func TruePeakLimit(buf [][]float32, sr int, ceilingDb float64) {
	if len(buf) == 0 || len(buf[0]) == 0 {
		return
	}
	n := len(buf[0])
	ceil := DbToLin(ceilingDb)

	// 必要ゲインの計算は各サンプルが独立なので、区間に分けて並列に求める
	need := make([]float32, n)
	workers := runtime.GOMAXPROCS(0)
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := min(lo+chunk, n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				p := 0.0
				for _, ch := range buf {
					p = math.Max(p, truePeak(ch, i))
				}
				if p > ceil {
					need[i] = float32(ceil / p)
				} else {
					need[i] = 1
				}
			}
		}()
	}
	wg.Wait()

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
		// 範囲外は1(ゲイン変化なし)として平均する
		sum := prefix[hi+1] - prefix[lo] + float64(2*look+1-(hi-lo+1))
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
