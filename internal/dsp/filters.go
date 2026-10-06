// Package dsp は信号処理の部品(Biquad、コンプ、歪み、分割FFT畳み込み、ラウドネス測定、リミッタ)を持つ。
// 音に関わる数値は呼び出し側(Projectのパラメーター)から受け取る。
// ここにある定数は、ユーザーが触らない処理方式そのものの形(Butterworth Q など)に限る。
package dsp

import "math"

// BlockSize は内部処理のブロックサイズ(畳み込みの分割単位)。
const BlockSize = 1024

// butterworthQ は2次Butterworthの Q。
const butterworthQ = 0.70710678118654752

func DbToLin(db float64) float64 { return math.Pow(10, db/20) }

func LinToDb(x float64) float64 { return 20 * math.Log10(math.Max(x, 1e-12)) }

// Biquad はRBJ cookbookの2次フィルタ(転置直接形II、状態はfloat64)。
type Biquad struct {
	b0, b1, b2, a1, a2 float64
	z1, z2             float64
}

func newBiquad(b0, b1, b2, a0, a1, a2 float64) *Biquad {
	return &Biquad{b0: b0 / a0, b1: b1 / a0, b2: b2 / a0, a1: a1 / a0, a2: a2 / a0}
}

func clampFreq(fs, f0 float64) float64 {
	return math.Min(math.Max(f0, 1), fs*0.49)
}

func HighPass(fs, f0 float64) *Biquad {
	w := 2 * math.Pi * clampFreq(fs, f0) / fs
	c, alpha := math.Cos(w), math.Sin(w)/(2*butterworthQ)
	return newBiquad((1+c)/2, -(1 + c), (1+c)/2, 1+alpha, -2*c, 1-alpha)
}

func LowPass(fs, f0 float64) *Biquad {
	w := 2 * math.Pi * clampFreq(fs, f0) / fs
	c, alpha := math.Cos(w), math.Sin(w)/(2*butterworthQ)
	return newBiquad((1-c)/2, 1-c, (1-c)/2, 1+alpha, -2*c, 1-alpha)
}

// HighShelf は shelf slope S=1 の高域シェルフ。
func HighShelf(fs, f0, gainDb float64) *Biquad {
	A := math.Pow(10, gainDb/40)
	w := 2 * math.Pi * clampFreq(fs, f0) / fs
	c, s := math.Cos(w), math.Sin(w)
	alpha := s / 2 * math.Sqrt2 // S=1 のとき (A+1/A)(1/S-1)+2 = 2
	sq := 2 * math.Sqrt(A) * alpha
	return newBiquad(
		A*((A+1)+(A-1)*c+sq),
		-2*A*((A-1)+(A+1)*c),
		A*((A+1)+(A-1)*c-sq),
		(A+1)-(A-1)*c+sq,
		2*((A-1)-(A+1)*c),
		(A+1)-(A-1)*c-sq,
	)
}

// LowShelf は shelf slope S=1 の低域シェルフ(f0 以下を gainDb だけ上げ下げする)。
func LowShelf(fs, f0, gainDb float64) *Biquad {
	A := math.Pow(10, gainDb/40)
	w := 2 * math.Pi * clampFreq(fs, f0) / fs
	c, s := math.Cos(w), math.Sin(w)
	alpha := s / 2 * math.Sqrt2 // S=1 のとき (A+1/A)(1/S-1)+2 = 2
	sq := 2 * math.Sqrt(A) * alpha
	return newBiquad(
		A*((A+1)-(A-1)*c+sq),
		2*A*((A-1)-(A+1)*c),
		A*((A+1)-(A-1)*c-sq),
		(A+1)+(A-1)*c+sq,
		-2*((A-1)+(A+1)*c),
		(A+1)+(A-1)*c-sq,
	)
}

func (b *Biquad) ProcessSample(x float64) float64 {
	y := b.b0*x + b.z1
	b.z1 = b.b1*x - b.a1*y + b.z2
	b.z2 = b.b2*x - b.a2*y
	return y
}

// Process はxをその場でフィルタする。
func (b *Biquad) Process(x []float32) {
	for i, v := range x {
		x[i] = float32(b.ProcessSample(float64(v)))
	}
}

// LR4 は4次のLinkwitz-Riley(2次Butterworthを2段)。状態を持つので、信号を区切って順に Process してよい
// (区切り方に結果は依らない)。同じ周波数の低域通過と高域通過の合計は、位相はずれるが振幅はフラットになる。
type LR4 struct{ s1, s2 *Biquad }

// NewLR4LowPass は4次のLinkwitz-Riley低域通過を返す。
func NewLR4LowPass(fs, f0 float64) *LR4 { return &LR4{s1: LowPass(fs, f0), s2: LowPass(fs, f0)} }

// NewLR4HighPass は4次のLinkwitz-Riley高域通過を返す。
func NewLR4HighPass(fs, f0 float64) *LR4 { return &LR4{s1: HighPass(fs, f0), s2: HighPass(fs, f0)} }

// Process はxをその場でフィルタする。
func (f *LR4) Process(x []float32) {
	f.s1.Process(x)
	f.s2.Process(x)
}

// LR4LowPass は4次のLinkwitz-Riley低域通過(2次Butterworthを2段)をその場で掛ける。
// 同じ周波数の LR4HighPass との合計は、位相はずれるが振幅はフラット(全域通過)になる。
func LR4LowPass(x []float32, fs, f0 float64) { NewLR4LowPass(fs, f0).Process(x) }

// LR4HighPass は4次のLinkwitz-Riley高域通過をその場で掛ける。
func LR4HighPass(x []float32, fs, f0 float64) { NewLR4HighPass(fs, f0).Process(x) }
