package dsp

import (
	"context"

	"gonum.org/v1/gonum/dsp/fourier"
)

// Convolve は x と ir の線形畳み込み(長さ len(x)+len(ir)-1)を、
// ブロックサイズ BlockSize の一様分割FFT畳み込み(overlap-save)で求める。
// 会場IRのような数秒のIRでも直接畳み込みより桁違いに速い。ctx がキャンセルされたら中断する。
func Convolve(ctx context.Context, x, ir []float32) ([]float32, error) {
	if len(x) == 0 || len(ir) == 0 {
		return []float32{}, nil
	}
	const B = BlockSize
	const N = 2 * B
	const nc = N/2 + 1
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
	scale := 1.0 / N

	for k := 0; k < blocks; k++ {
		if k%64 == 0 {
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
