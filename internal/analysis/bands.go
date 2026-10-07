// Package analysis は、音を時間ごとの1/3オクターブ帯域のレベルにする。
//
// スペクトラム表示で「PAから出た音」と「耳に届く音」を重ねて見るため、PA出力の帯域レベルを
// 処理側(Go)で曲全体について先に求めて渡す。計算は、フロントが再生中の音を AnalyserNode で測るのと
// 同じ式にそろえてある(帯域の分け方・窓・補正・下限。フロントの spectrum.ts と対応させること)。
package analysis

import (
	"context"
	"encoding/binary"
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

// Analyzer は mono(サンプルレート sr)を、信号を区切って順に Write しながら、HopSec ごとのフレームについて
// 帯域レベルにする。フレーム f は [f×hop-WindowSize/2, f×hop+WindowSize/2) の窓で、中心は f×HopSec。
// 信号の外は無音として扱う。窓がそろったフレームから計算し(フレームは独立なので並列)、
// 結果は、全体を一度に計算した場合と、区切り方に依らずビット単位で一致する。
type Analyzer struct {
	sr      int
	hop     int
	win     []float64
	hist    []float32 // 次のフレームの窓の左端から現在までの入力
	base    int       // hist[0] の絶対位置
	total   int       // 受け取ったフレーム数(サンプル数)
	next    int       // 次に計算するフレーム
	data    []float32
	workers []*analysisWorker
}

type analysisWorker struct {
	fft   *fourier.FFT
	frame []float64
	coef  []complex128
	power []float64
}

// NewAnalyzer はサンプルレート sr の信号用のアナライザーを返す。
func NewAnalyzer(sr int) *Analyzer {
	a := &Analyzer{sr: sr, hop: int(math.Round(HopSec * float64(sr))), win: blackman(WindowSize)}
	for range runtime.GOMAXPROCS(0) {
		a.workers = append(a.workers, &analysisWorker{
			fft:   fourier.NewFFT(WindowSize), // FFTは共有しない
			frame: make([]float64, WindowSize),
			coef:  make([]complex128, WindowSize/2+1),
			power: make([]float64, WindowSize/2+1),
		})
	}
	return a
}

// Write は mono の続きを与え、窓がそろったフレームを計算する。
func (a *Analyzer) Write(ctx context.Context, mono []float32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.hist = append(a.hist, mono...)
	a.total += len(mono)
	if a.total < WindowSize/2 {
		return nil
	}
	return a.compute(ctx, (a.total-WindowSize/2)/a.hop+1)
}

// Finish は入力の終わりを知らせ、残りのフレーム(窓の右半分が信号の外にはみ出すもの)を計算して返す。
// フレーム数は ceil(総サンプル数 / hop)。
func (a *Analyzer) Finish(ctx context.Context) (*Series, error) {
	frames := (a.total + a.hop - 1) / a.hop
	if err := a.compute(ctx, frames); err != nil {
		return nil, err
	}
	data := a.data
	if data == nil {
		data = []float32{}
	}
	return &Series{HopSec: float64(a.hop) / float64(a.sr), Bands: NumBands(), Frames: frames, Data: data}, nil
}

// compute はフレーム [next, upTo) を計算する(upTo は、窓がそろっているか、信号の終わりが確定しているもの)。
func (a *Analyzer) compute(ctx context.Context, upTo int) error {
	from := a.next
	if upTo > from {
		a.data = append(a.data, make([]float32, (upTo-from)*NumBands())...)
		workers := min(len(a.workers), upTo-from)
		chunk := (upTo - from + workers - 1) / workers
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo, hi := from+w*chunk, min(from+(w+1)*chunk, upTo)
			wg.Add(1)
			go func() {
				defer wg.Done()
				wk := a.workers[w]
				for f := lo; f < hi; f++ {
					if f%64 == 0 && ctx.Err() != nil {
						return
					}
					start := f*a.hop - WindowSize/2
					for i := range wk.frame {
						if j := start + i; j >= 0 && j < a.total {
							wk.frame[i] = float64(a.hist[j-a.base]) * a.win[i]
						} else {
							wk.frame[i] = 0
						}
					}
					wk.fft.Coefficients(wk.coef, wk.frame)
					for k, c := range wk.coef {
						// AnalyserNode と同じ: |X[k]| = |Σ x·w·e^-jωn| / N のパワー
						re, im := real(c)/WindowSize, imag(c)/WindowSize
						wk.power[k] = re*re + im*im
					}
					bandLevels(wk.power, a.sr, a.data[f*NumBands():(f+1)*NumBands()])
				}
			}()
		}
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		a.next = upTo
	}
	// 次のフレームの窓の左端より前は、もう使わない
	if keep := max(a.next*a.hop-WindowSize/2, 0); keep > a.base {
		a.hist = a.hist[:copy(a.hist, a.hist[keep-a.base:])]
		a.base = keep
	}
	return nil
}

// Compute は mono(サンプルレート sr)を、HopSec ごとのフレームについて帯域レベルにする。
// フレームは中心を f×HopSec にそろえ、信号の外は無音として扱う。Analyzer に全体を流す包み。
func Compute(ctx context.Context, mono []float32, sr int) (*Series, error) {
	a := NewAnalyzer(sr)
	if err := a.Write(ctx, mono); err != nil {
		return nil, err
	}
	return a.Finish(ctx)
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

// MeanSquareAcc は信号の2乗平均を、区切って順に Add しながら求める(全体を一度に求めた場合と同じ値)。
type MeanSquareAcc struct {
	sum float64
	n   int
}

// Add は x の続きを加える。
func (m *MeanSquareAcc) Add(x []float32) {
	for _, v := range x {
		m.sum += float64(v) * float64(v)
	}
	m.n += len(x)
}

// Value はここまでの2乗平均。空なら 0。
func (m *MeanSquareAcc) Value() float64 {
	if m.n == 0 {
		return 0
	}
	return m.sum / float64(m.n)
}

// MeanSquare は信号の2乗平均。
func MeanSquare(x []float32) float64 {
	var m MeanSquareAcc
	m.Add(x)
	return m.Value()
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

// Bytes は Data を、リトルエンディアンの float32 の並び(フレーム × 帯域、行優先)にする。
// フロントは Float32Array としてそのまま読める。
func (s *Series) Bytes() []byte {
	b := make([]byte, 4*len(s.Data))
	for i, v := range s.Data {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}
