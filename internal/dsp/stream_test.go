package dsp

import (
	"math/rand"
	"testing"
)

// chunkRanges は長さ n を size ずつの区間に分ける(size<=0 なら全体で1つ)。
func chunkRanges(n, size int) [][2]int {
	if size <= 0 || size >= n {
		return [][2]int{{0, n}}
	}
	var out [][2]int
	for a := 0; a < n; a += size {
		out = append(out, [2]int{a, min(a+size, n)})
	}
	return out
}

func equalF32(a, b []float32) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		if a[i] != b[i] {
			return i, false
		}
	}
	return 0, true
}

func copyBuf(b [][]float32) [][]float32 {
	out := make([][]float32, len(b))
	for i := range b {
		out[i] = append([]float32(nil), b[i]...)
	}
	return out
}

func TestLR4StreamMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	x := randSignal(rng, 20000)
	for _, hp := range []bool{false, true} {
		want := append([]float32(nil), x...)
		if hp {
			refLR4HighPass(want, 48000, 90)
		} else {
			refLR4LowPass(want, 48000, 90)
		}
		for _, size := range []int{1, 7, 1023, 4096, 0} {
			got := append([]float32(nil), x...)
			var f *LR4
			if hp {
				f = NewLR4HighPass(48000, 90)
			} else {
				f = NewLR4LowPass(48000, 90)
			}
			for _, r := range chunkRanges(len(got), size) {
				f.Process(got[r[0]:r[1]])
			}
			if i, ok := equalF32(got, want); !ok {
				t.Fatalf("hp=%v chunk=%d: differs at %d", hp, size, i)
			}
		}
	}
}

func TestCompressorStreamMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	n := 30000
	buf := [][]float32{randSignal(rng, n), randSignal(rng, n)}
	for i := range buf[0] {
		buf[0][i] *= 1.2
		if i%7000 < 2000 {
			buf[1][i] *= 0.05
		}
	}
	for _, p := range []CompParams{
		{ThresholdDb: -18, Ratio: 4, AttackMs: 10, ReleaseMs: 120},
		{ThresholdDb: -30, Ratio: 8, AttackMs: 0, ReleaseMs: 5},
		{ThresholdDb: -18, Ratio: 1, AttackMs: 10, ReleaseMs: 120},
		{ThresholdDb: -18, Ratio: 0.5, AttackMs: 10, ReleaseMs: 120},
	} {
		want := copyBuf(buf)
		refCompress(want, 48000, p)
		for _, size := range []int{1, 7, 1023, 4096, 0} {
			got := copyBuf(buf)
			c := NewCompressor(48000, p)
			for _, r := range chunkRanges(n, size) {
				c.Process([][]float32{got[0][r[0]:r[1]], got[1][r[0]:r[1]]})
			}
			for ch := range want {
				if i, ok := equalF32(got[ch], want[ch]); !ok {
					t.Fatalf("%+v chunk=%d ch%d: differs at %d", p, size, ch, i)
				}
			}
		}
	}
	// チャンネルなし・長さ0でも落ちない
	NewCompressor(48000, CompParams{Ratio: 4}).Process(nil)
	NewCompressor(48000, CompParams{Ratio: 4}).Process([][]float32{{}, {}})
}
