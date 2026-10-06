package dsp

// 処理時間のベンチマーク。TOTTEMOLIVE_LONG_TESTS=1 のときだけ動く(4分ぶんの入力)。
//
//	TOTTEMOLIVE_LONG_TESTS=1 go test ./internal/dsp -run '^$' -bench . -count 10 -cpu 4,16

import (
	"math/rand"
	"os"
	"testing"
)

const (
	benchFrames = 4 * 60 * 48000
	benchChunk  = 65536
)

func requireLongBench(b *testing.B) {
	b.Helper()
	if os.Getenv("TOTTEMOLIVE_LONG_TESTS") == "" {
		b.Skip("TOTTEMOLIVE_LONG_TESTS=1 のときだけ実行する")
	}
}

func benchNoise(n int, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	x := make([]float32, n)
	for i := range x {
		x[i] = (r.Float32()*2 - 1) * 0.3
	}
	return x
}

// benchStream は NewStreamConvolver(irs...) に4分ぶんを65536ずつ流して Flush する。
func benchStream(b *testing.B, irs ...[]float32) {
	requireLongBench(b)
	x := benchNoise(benchChunk, 1)
	for i := 0; i < b.N; i++ {
		c := NewStreamConvolver(irs...)
		for from := 0; from < benchFrames; from += benchChunk {
			c.Process(x)
		}
		c.Flush()
	}
}

func BenchmarkStreamShortIR(b *testing.B)     { benchStream(b, benchNoise(400, 2)) }
func BenchmarkStreamShortIRPair(b *testing.B) { benchStream(b, benchNoise(400, 2), benchNoise(400, 3)) }

// 会場IR相当(約2.5秒)。
func BenchmarkStreamLongIR(b *testing.B) { benchStream(b, benchNoise(120000, 4)) }
func BenchmarkStreamLongIRPair(b *testing.B) {
	benchStream(b, benchNoise(120000, 4), benchNoise(120000, 5))
}

func BenchmarkCompressor(b *testing.B) {
	requireLongBench(b)
	src := [][]float32{benchNoise(benchChunk, 6), benchNoise(benchChunk, 7)}
	buf := [][]float32{make([]float32, benchChunk), make([]float32, benchChunk)}
	for i := 0; i < b.N; i++ {
		c := NewCompressor(48000, CompParams{ThresholdDb: -18, Ratio: 3, AttackMs: 10, ReleaseMs: 150})
		for from := 0; from < benchFrames; from += benchChunk {
			copy(buf[0], src[0])
			copy(buf[1], src[1])
			c.Process(buf)
		}
	}
}
