package render

import (
	"math/rand"
	"runtime"
	"testing"

	"tottemolive/internal/project"
)

func randStereo(rng *rand.Rand, n int, amp float32) [][]float32 {
	b := [][]float32{make([]float32, n), make([]float32, n)}
	for c := range b {
		for i := range b[c] {
			b[c][i] = (rng.Float32()*2 - 1) * amp
		}
	}
	return b
}

func cloneStereo(b [][]float32) [][]float32 {
	return [][]float32{append([]float32(nil), b[0]...), append([]float32(nil), b[1]...)}
}

// paProc は、チャンネル・サンプル方向に並列化しても、全体を一度に処理する元の関数(legacyApplyPA)と
// ビット単位で一致する(並列度・チャンクの分け方・しきい値をまたぐ長さで)。
func TestPAProcParallelMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(51))
	pa := project.New().PA
	for _, n := range []int{1, 4095, 4096, 4097, 70000, 150001} {
		in := randStereo(rng, n, 0.9)
		want := cloneStereo(in)
		legacyApplyPA(want, sampleRate, 3, pa)
		for _, procs := range []int{1, 2, 3, 16} {
			old := runtime.GOMAXPROCS(procs)
			for _, size := range []int{999, 4096, 65536, 1 << 20} {
				got := cloneStereo(in)
				proc := newPAProc(sampleRate, 3, pa)
				for from := 0; from < n; from += size {
					end := min(from+size, n)
					proc.Process([][]float32{got[0][from:end], got[1][from:end]})
				}
				if m, d := compareAudioQuiet(got, want); d > 0 {
					t.Errorf("n=%d procs=%d chunk=%d: %d samples differ (max %g)", n, procs, size, d, m)
				}
			}
			runtime.GOMAXPROCS(old)
		}
	}
}

// radiatedProc は、3本のLR4を並列に回しても、元の関数(legacyRadiatedMono)とビット単位で一致する。
func TestRadiatedProcParallelMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(52))
	sub := project.Sub{CrossoverHz: 90, LevelDb: 3}
	for _, active := range []bool{true, false} {
		for _, n := range []int{1, 4095, 4096, 70000, 150001} {
			in := randStereo(rng, n, 0.8)
			want := legacyRadiatedMono(in, active, sub)
			for _, procs := range []int{1, 2, 3, 16} {
				old := runtime.GOMAXPROCS(procs)
				for _, size := range []int{999, 4096, 65536} {
					r := newRadiatedProc(active, sub)
					var got []float32
					for from := 0; from < n; from += size {
						end := min(from+size, n)
						got = append(got, r.Process([][]float32{in[0][from:end], in[1][from:end]})...)
					}
					if m, d := compareAudioQuiet([][]float32{got}, [][]float32{want}); d > 0 {
						t.Errorf("active=%v n=%d procs=%d chunk=%d: %d samples differ (max %g)", active, n, procs, size, d, m)
					}
				}
				runtime.GOMAXPROCS(old)
			}
		}
	}
}

// compareAudioQuiet は2つの出力の最大差と不一致サンプル数を返す(ログなし)。長さが違えば全部不一致。
func compareAudioQuiet(got, want [][]float32) (maxDiff float64, mismatches int) {
	for c := range want {
		if len(got[c]) != len(want[c]) {
			return 0, len(want[c]) + 1
		}
		for i := range want[c] {
			if got[c][i] != want[c][i] {
				mismatches++
				d := float64(got[c][i]) - float64(want[c][i])
				if d < 0 {
					d = -d
				}
				maxDiff = max(maxDiff, d)
			}
		}
	}
	return maxDiff, mismatches
}
