package dsp

import (
	"math"
	"math/rand"
	"runtime"
	"testing"
)

// withProcs は GOMAXPROCS を n にして f を呼び、戻す。
func withProcs(t *testing.T, n int, f func()) {
	t.Helper()
	old := runtime.GOMAXPROCS(n)
	defer runtime.GOMAXPROCS(old)
	f()
}

var parallelProcs = []int{1, 2, 3, 16}

func TestParallelForCoversRange(t *testing.T) {
	for _, procs := range parallelProcs {
		withProcs(t, procs, func() {
			for _, n := range []int{0, 1, 7, 1023, 1024, 2047, 2048, 2049, 5000, 65536} {
				hit := make([]int, n)
				parallelFor(n, 1024, func(lo, hi int) {
					for i := lo; i < hi; i++ {
						hit[i]++
					}
				})
				for i, h := range hit {
					if h != 1 {
						t.Fatalf("procs=%d n=%d: index %d visited %d times", procs, n, i, h)
					}
				}
			}
		})
	}
}

// コンプは、並列度・チャンク分け・しきい値をまたぐ長さに依らず、参照実装(元の1重ループ)とビット単位で一致する。
func TestCompressorParallelMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	p := CompParams{ThresholdDb: -24, Ratio: 4, AttackMs: 5, ReleaseMs: 80}
	for _, n := range []int{1, 1023, 2047, 2048, 2049, 4097, compBlock - 1, compBlock, compBlock + 1, 2*compBlock + 123} {
		buf := [][]float32{randSignal(rng, n), randSignal(rng, n)}
		for i := range buf[0] {
			buf[0][i] *= 1.3
			if (i/5000)%3 == 0 {
				buf[1][i] *= 0.02 // 静かな区間(リリースが効く)
			}
		}
		want := copyBuf(buf)
		refCompress(want, 48000, p)
		for _, procs := range parallelProcs {
			withProcs(t, procs, func() {
				for _, size := range []int{0, 1999, 2048, 4097, compBlock, 100000} {
					got := copyBuf(buf)
					c := NewCompressor(48000, p)
					for _, r := range chunkRanges(n, size) {
						c.Process([][]float32{got[0][r[0]:r[1]], got[1][r[0]:r[1]]})
					}
					for ch := range want {
						if i, ok := equalF32(got[ch], want[ch]); !ok {
							t.Fatalf("n=%d procs=%d chunk=%d ch%d: differs at %d", n, procs, size, ch, i)
						}
					}
				}
			})
		}
	}
}

func refSaturate(buf [][]float32, drive float64) {
	if drive <= 0 {
		return
	}
	k := 1 + drive*(driveMaxK-1)
	for _, ch := range buf {
		for i, v := range ch {
			ch[i] = float32(math.Tanh(k*float64(v)) / k)
		}
	}
}

func TestSaturateParallelMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for _, n := range []int{0, 1, 4095, 2 * satMinPer, 2*satMinPer + 1, 100003} {
		buf := [][]float32{randSignal(rng, n), randSignal(rng, n)}
		want := copyBuf(buf)
		refSaturate(want, 0.6)
		for _, procs := range parallelProcs {
			withProcs(t, procs, func() {
				got := copyBuf(buf)
				Saturate(got, 0.6)
				for ch := range want {
					if i, ok := equalF32(got[ch], want[ch]); !ok {
						t.Fatalf("n=%d procs=%d ch%d: differs at %d", n, procs, ch, i)
					}
				}
			})
		}
	}
	Saturate(nil, 0.5)
}
