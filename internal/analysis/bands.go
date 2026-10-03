// Package analysis は、音を時間ごとの1/3オクターブ帯域のレベルにする。
//
// スペクトラム表示で「PAから出た音」と「耳に届く音」を重ねて見るため、PA出力の帯域レベルを
// 処理側(Go)で曲全体について先に求めて渡す。計算は、フロントが再生中の音を AnalyserNode で測るのと
// 同じ式にそろえてある(帯域の分け方・窓・補正・下限。フロントの spectrum.ts と対応させること)。
package analysis

import (
	"context"
	"math"
	"runtime"
	"sync"

	"gonum.org/v1/gonum/dsp/fourier"
)

const (
	// WindowSize は1フレームのFFT長。48 kHz で bin幅 約2.9 Hz(低域の帯域まで分解できる)、窓の長さ 約0.34 秒。
	WindowSize = 16384
	// HopSec はフレームの間隔(秒)。表示は再生位置に合わせて、フレームの間を補間する。
	HopSec = 0.05
	// FloorDb は下限(dB)。これ以下は無音として扱う。
	FloorDb = -90
	// CalibrationDb は、0 dBFS の正弦波が 0 dB になるようにする補正。Blackman窓の2乗平均(0.3046)と、
	// 正のbinが半分のパワーを持つことから 10·log10(1 / (0.3046 / 4)) ≒ 11.2。
	CalibrationDb = 11.2
)

// BandCenters はISOの1/3オクターブ中心周波数(Hz)、20 Hz〜20 kHz の31帯域。
var BandCenters = []float64{
	20, 25, 31.5, 40, 50, 63, 80, 100, 125, 160, 200, 250, 315, 400, 500, 630, 800, 1000, 1250, 1600, 2000, 2500,
	3150, 4000, 5000, 6300, 8000, 10000, 12500, 16000, 20000,
}

// NumBands は帯域の数。
func NumBands() int { return len(BandCenters) }

// bandEdges は帯域の下端・上端(中心の 2^(±1/6) 倍)。
func bandEdges(center float64) (lo, hi float64) {
	r := math.Pow(2, 1.0/6)
	return center / r, center * r
}

// Series は帯域レベルの時系列。Data は フレーム × NumBands の行優先(dB、0 dBFS の正弦波 = 0 dB)で、
// フレーム f の中心は f × HopSec 秒。
type Series struct {
	HopSec float64
	Bands  int
	Frames int
	Data   []float32
}

// blackman はWeb AudioのAnalyserNodeと同じBlackman窓(α = 0.16)。
func blackman(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)) + 0.08*math.Cos(4*math.Pi*float64(i)/float64(n))
	}
	return w
}

// Compute は mono(サンプルレート sr)を、HopSec ごとのフレームについて帯域レベルにする。
// フレームは中心を f×HopSec にそろえ、信号の外は無音として扱う。フレームは独立なので並列に計算する。
func Compute(ctx context.Context, mono []float32, sr int) (*Series, error) {
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

// bandLevels はbinごとのパワーから、帯域ごとのレベル(dB)を out に書く。帯域内のbinのパワーを足し、
// 低域で帯域がbin幅より狭いときは、最も近いbinのパワーを帯域幅の比で換算する(フロントの bandLevels と同じ)。
func bandLevels(power []float64, sr int, out []float32) {
	binHz := float64(sr) / WindowSize
	for i, fc := range BandCenters {
		lo, hi := bandEdges(fc)
		from := max(1, int(math.Ceil(lo/binHz)))
		to := min(len(power)-1, int(math.Ceil(hi/binHz))-1)
		p := 0.0
		if to >= from {
			for k := from; k <= to; k++ {
				p += power[k]
			}
		} else {
			k := min(len(power)-1, max(1, int(math.Round(fc/binHz))))
			p = power[k] * (hi - lo) / binHz
		}
		db := float64(FloorDb)
		if p > 0 {
			db = math.Max(FloorDb, 10*math.Log10(p)+CalibrationDb)
		}
		out[i] = float32(db)
	}
}

// MeanSquare は信号の2乗平均。
func MeanSquare(x []float32) float64 {
	if len(x) == 0 {
		return 0
	}
	s := 0.0
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return s / float64(len(x))
}

// Mono は左右の平均(フロントのAnalyserNodeが、ステレオをモノにするのと同じ)。長さは短いほうにそろえる。
func Mono(l, r []float32) []float32 {
	n := min(len(l), len(r))
	out := make([]float32, n)
	for i := range out {
		out[i] = (l[i] + r[i]) / 2
	}
	return out
}
