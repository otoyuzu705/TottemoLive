package dsp

import (
	"context"

	"gonum.org/v1/gonum/dsp/fourier"
)

// 畳み込みのブロックサイズの選び方。オフライン処理で遅延の制約がないので、
// FFTの回数と積和の量の釣り合いが取れる大きさをIR長から決める(会場IRで8192が最適、
// それ以上は変わらないことを測定済み)。
const (
	shortIRMax   = 512  // これ以下のIRは1つのFFTで済ませる
	shortFFTSize = 4096 // 短いIR用のFFT長
	maxPartition = 8192 // 長いIRの分割サイズの上限
	partitionDiv = 16   // 分割サイズ ≈ IR長 / partitionDiv(2のべき乗に切り上げ)
)

// Convolve は x と ir の線形畳み込み(長さ len(x)+len(ir)-1)を、FFTによる畳み込み(overlap-save)で求める。
// 長いIR(会場IR)は一様分割して周波数領域の遅延線で畳み込み、短いIR(HRIR)は1回のFFTで処理する。
// ctx がキャンセルされたら中断する。
func Convolve(ctx context.Context, x, ir []float32) ([]float32, error) {
	if len(x) == 0 || len(ir) == 0 {
		return []float32{}, nil
	}
	if len(ir) <= shortIRMax {
		return convolveShort(ctx, x, ir)
	}
	return convolvePartitioned(ctx, x, ir, partitionSize(len(ir)))
}

// partitionSize は長いIRの分割サイズ(2のべき乗、BlockSize〜maxPartition)。
func partitionSize(irLen int) int {
	b := BlockSize
	for b < irLen/partitionDiv && b < maxPartition {
		b *= 2
	}
	return b
}

// convolveShort は短いIR用の古典的なoverlap-save。FFT長 N に対し、1回に N-L+1 サンプルを出す。
func convolveShort(ctx context.Context, x, ir []float32) ([]float32, error) {
	L := len(ir)
	N := shortFFTSize
	B := N - L + 1
	fft := fourier.NewFFT(N)
	nc := N/2 + 1
	buf := make([]float64, N)
	for i := range ir {
		buf[i] = float64(ir[i])
	}
	H := fft.Coefficients(make([]complex128, nc), buf)
	outLen := len(x) + L - 1
	out := make([]float32, outLen)
	X := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)
	for start, blk := 0, 0; start < outLen; start, blk = start+B, blk+1 {
		if blk%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// フレームは x[start-(L-1) .. start-(L-1)+N)。先頭の L-1 点は巡回で汚れるので捨てる
		for i := 0; i < N; i++ {
			if j := start - (L - 1) + i; j >= 0 && j < len(x) {
				buf[i] = float64(x[j])
			} else {
				buf[i] = 0
			}
		}
		fft.Coefficients(X, buf)
		for i := range X {
			X[i] *= H[i]
		}
		fft.Sequence(y, X)
		for i := 0; i < B && start+i < outLen; i++ {
			out[start+i] = float32(y[L-1+i] * scale)
		}
	}
	return out, nil
}

// convolvePartitioned は IR を B 点ずつに分け、FFT長 2B の周波数領域遅延線で畳み込む。
func convolvePartitioned(ctx context.Context, x, ir []float32, B int) ([]float32, error) {
	N := 2 * B
	nc := N/2 + 1
	fft := fourier.NewFFT(N)

	// IRをB点ずつに分けてスペクトルにしておく
	parts := (len(ir) + B - 1) / B
	H := make([][]complex128, parts)
	buf := make([]float64, N)
	for p := 0; p < parts; p++ {
		for i := range buf {
			buf[i] = 0
		}
		for i := 0; i < B && p*B+i < len(ir); i++ {
			buf[i] = float64(ir[p*B+i])
		}
		H[p] = fft.Coefficients(make([]complex128, nc), buf)
	}

	outLen := len(x) + len(ir) - 1
	blocks := (outLen + B - 1) / B
	out := make([]float32, outLen)

	// 周波数領域の遅延線(新しい入力スペクトルほど小さい添字)
	fdl := make([][]complex128, parts)
	for i := range fdl {
		fdl[i] = make([]complex128, nc)
	}
	acc := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)

	for k := 0; k < blocks; k++ {
		if k%16 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// 入力フレーム = [前のブロック, 今のブロック]
		for i := 0; i < N; i++ {
			j := (k-1)*B + i
			if j >= 0 && j < len(x) {
				buf[i] = float64(x[j])
			} else {
				buf[i] = 0
			}
		}
		// 最古のスペクトルの配列を再利用して新しいスペクトルを入れる
		oldest := fdl[parts-1]
		copy(fdl[1:], fdl[:parts-1])
		fdl[0] = fft.Coefficients(oldest, buf)

		for i := range acc {
			acc[i] = 0
		}
		for p := 0; p < parts; p++ {
			a, h := fdl[p], H[p]
			for i := 0; i < nc; i++ {
				acc[i] += a[i] * h[i]
			}
		}
		fft.Sequence(y, acc)
		for i := 0; i < B; i++ {
			o := k*B + i
			if o >= outLen {
				break
			}
			out[o] = float32(y[B+i] * scale)
		}
	}
	return out, nil
}
