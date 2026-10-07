package analysis

// 参照実装。チャンク処理(ストリーミング化)の前の、曲全体を一度に処理する Compute を一字一句そのまま残したもの。
// 流し処理版(Analyzer)は、これとビット単位で一致することをテストで確かめる。

import (
	"context"
	"math"
	"runtime"
	"sync"

	"gonum.org/v1/gonum/dsp/fourier"
)

func refCompute(ctx context.Context, mono []float32, sr int) (*Series, error) {
	hop := int(math.Round(HopSec * float64(sr)))
	frames := (len(mono) + hop - 1) / hop
	s := &Series{HopSec: float64(hop) / float64(sr), Bands: NumBands(), Frames: frames, Data: make([]float32, frames*NumBands())}
	if frames == 0 {
		return s, nil
	}
	win := blackman(WindowSize)
	workers := runtime.GOMAXPROCS(0)
	chunk := (frames + workers - 1) / workers
	var wg sync.WaitGroup
	for lo := 0; lo < frames; lo += chunk {
		hi := min(lo+chunk, frames)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fft := fourier.NewFFT(WindowSize) // FFTは共有しない
			frame := make([]float64, WindowSize)
			coef := make([]complex128, WindowSize/2+1)
			power := make([]float64, WindowSize/2+1)
			for f := lo; f < hi; f++ {
				if f%64 == 0 && ctx.Err() != nil {
					return
				}
				start := f*hop - WindowSize/2
				for i := range frame {
					if j := start + i; j >= 0 && j < len(mono) {
						frame[i] = float64(mono[j]) * win[i]
					} else {
						frame[i] = 0
					}
				}
				fft.Coefficients(coef, frame)
				for k, c := range coef {
					// AnalyserNode と同じ: |X[k]| = |Σ x·w·e^-jωn| / N のパワー
					re, im := real(c)/WindowSize, imag(c)/WindowSize
					power[k] = re*re + im*im
				}
				bandLevels(power, sr, s.Data[f*s.Bands:(f+1)*s.Bands])
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
