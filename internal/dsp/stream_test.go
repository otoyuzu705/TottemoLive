package dsp

import (
	"context"
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

func TestLoudnessMeterMatchesRef(t *testing.T) {
	const sr = 48000
	rng := rand.New(rand.NewSource(13))
	loud := func(n int) [][]float32 {
		x := randSignal(rng, n)
		y := randSignal(rng, n)
		for i := range x { // 音量が時間で変わる(ゲートが効く)
			g := float32(0.05 + 0.4*float64((i/9000)%5)/4)
			x[i] *= g
			y[i] *= g * 0.7
		}
		return [][]float32{x, y}
	}
	lengths := []int{0, 1, 4799, 4800*4 - 1, 4800 * 4, 19200, 19201, 10 * sr}
	for _, n := range lengths {
		for _, kind := range []string{"noise", "silence", "mono"} {
			var buf [][]float32
			switch kind {
			case "noise":
				buf = loud(n)
			case "silence":
				buf = [][]float32{make([]float32, n), make([]float32, n)}
			case "mono":
				buf = loud(n)[:1]
			}
			want := refIntegratedLUFS(buf, sr)
			if got := IntegratedLUFS(buf, sr); got != want {
				t.Errorf("n=%d %s: IntegratedLUFS %v want %v", n, kind, got, want)
			}
			for _, size := range []int{1, 7, 4800, 4801, 8192, 20000, 0} {
				if size == 1 && n > 30000 {
					continue
				}
				m := NewLoudnessMeter(sr, len(buf))
				for _, r := range chunkRanges(n, size) {
					part := make([][]float32, len(buf))
					for c := range buf {
						part[c] = buf[c][r[0]:r[1]]
					}
					m.Write(part)
					m.Integrated() // 途中で測っても状態を壊さない
				}
				if got := m.Integrated(); got != want {
					t.Errorf("n=%d %s chunk=%d: %v want %v", n, kind, size, got, want)
				}
			}
		}
	}
}

// convolvePartitionedAll は分割サイズ B を指定して、全体を分割畳み込みする(テスト用)。
func convolvePartitionedAll(ctx context.Context, x, ir []float32, B int) ([]float32, error) {
	out, err := runStream(ctx, newPartitionedStream(ir, B), x, [][]float32{ir})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// streamAll は x を size ずつ c に流して、IRごとの全出力を返す。
func streamAll(c *StreamConvolver, x []float32, size int, nIR int) [][]float32 {
	outs := make([][]float32, nIR)
	add := func(ys [][]float32) {
		for k, y := range ys {
			outs[k] = append(outs[k], y...)
		}
	}
	if len(x) > 0 {
		for _, r := range chunkRanges(len(x), size) {
			add(c.Process(x[r[0]:r[1]]))
		}
	}
	add(c.Flush())
	return outs
}

func TestStreamConvolverMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	ctx := context.Background()
	irLensList := []int{1, 192, 446, 512, 513, 5000, 100000}
	for _, L := range irLensList {
		ir := randSignal(rng, L)
		B := shortFFTSize - L + 1
		if L > shortIRMax {
			B = partitionSize(L)
		}
		inLens := []int{0, 1, B - 1, B, B + 1, 3*B + 7, 20000}
		for _, n := range inLens {
			if n < 0 {
				continue
			}
			x := randSignal(rng, n)
			want, err := refConvolve(ctx, x, ir)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Convolve(ctx, x, ir)
			if err != nil {
				t.Fatal(err)
			}
			if i, ok := equalF32(got, want); !ok {
				t.Fatalf("Convolve L=%d n=%d: differs at %d (len %d vs %d)", L, n, i, len(got), len(want))
			}
			for _, size := range []int{1, B - 1, B, B + 1, 65536} {
				if size < 1 || (size == 1 && n > 3000) {
					continue
				}
				outs := streamAll(NewStreamConvolver(ir), x, size, 1)
				if i, ok := equalF32(outs[0], want); !ok {
					t.Fatalf("stream L=%d n=%d chunk=%d: differs at %d (len %d vs %d)", L, n, size, i, len(outs[0]), len(want))
				}
			}
		}
	}
}

func TestStreamConvolverPairMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(22))
	ctx := context.Background()
	for _, c := range []struct{ na, nb int }{{192, 192}, {100, 400}, {1, 512}, {192, 3000}, {2000, 3000}} {
		a, b := randSignal(rng, c.na), randSignal(rng, c.nb)
		for _, n := range []int{0, 1, 3000, 12345} {
			x := randSignal(rng, n)
			wa, wb, err := refConvolvePair(ctx, x, a, b)
			if err != nil {
				t.Fatal(err)
			}
			ga, gb, err := ConvolvePair(ctx, x, a, b)
			if err != nil {
				t.Fatal(err)
			}
			if i, ok := equalF32(ga, wa); !ok {
				t.Fatalf("pair %+v n=%d A differs at %d", c, n, i)
			}
			if i, ok := equalF32(gb, wb); !ok {
				t.Fatalf("pair %+v n=%d B differs at %d", c, n, i)
			}
			for _, size := range []int{1000, 4097, 65536} {
				outs := streamAll(NewStreamConvolver(a, b), x, size, 2)
				if i, ok := equalF32(outs[0], wa); !ok {
					t.Fatalf("stream pair %+v n=%d chunk=%d A differs at %d", c, n, size, i)
				}
				if i, ok := equalF32(outs[1], wb); !ok {
					t.Fatalf("stream pair %+v n=%d chunk=%d B differs at %d", c, n, size, i)
				}
			}
		}
	}
}

// Flush の長さ: 入力 + len(ir) - 1、入力0なら空。Process の出力は入力より先に出ない。
func TestStreamConvolverLengths(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	for _, L := range []int{1, 100, 600, 9000} {
		ir := randSignal(rng, L)
		for _, n := range []int{0, 1, 500, 5000, 70000} {
			c := NewStreamConvolver(ir)
			got := 0
			fed := 0
			for _, r := range chunkRanges(n, 777) {
				if n == 0 {
					break
				}
				fed += r[1] - r[0]
				got += len(c.Process(make([]float32, r[1]-r[0]))[0])
				if got > fed {
					t.Fatalf("L=%d: output %d ahead of input %d", L, got, fed)
				}
			}
			got += len(c.Flush()[0])
			want := 0
			if n > 0 {
				want = n + L - 1
			}
			if got != want {
				t.Errorf("L=%d n=%d: total %d want %d", L, n, got, want)
			}
		}
	}
}

func TestStreamConvolverCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Convolve(ctx, make([]float32, 200000), make([]float32, 9000)); err == nil {
		t.Error("expected cancellation error")
	}
}
