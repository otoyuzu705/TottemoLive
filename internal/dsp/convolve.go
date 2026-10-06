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

// streamChunk は、Convolve / ConvolvePair が StreamConvolver に一度に流すフレーム数。
// 流す合間に ctx を確認する。
const streamChunk = 65536

// Convolve は x と ir の線形畳み込み(長さ len(x)+len(ir)-1)を、FFTによる畳み込み(overlap-save)で求める。
// 長いIR(会場IR)は一様分割して周波数領域の遅延線で畳み込み、短いIR(HRIR)は1回のFFTで処理する。
// ctx がキャンセルされたら中断する。StreamConvolver に流す包み。
func Convolve(ctx context.Context, x, ir []float32) ([]float32, error) {
	if len(x) == 0 || len(ir) == 0 {
		return []float32{}, nil
	}
	out, err := runStream(ctx, NewStreamConvolver(ir), x, [][]float32{ir})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

// partitionSize は長いIRの分割サイズ(2のべき乗、BlockSize〜maxPartition)。
func partitionSize(irLen int) int {
	b := BlockSize
	for b < irLen/partitionDiv && b < maxPartition {
		b *= 2
	}
	return b
}

// ConvolvePair は同じ入力 x を2つのIR(左右の耳など)で畳み込む。IRが両方とも短いときは、
// 入力のFFTを1回にまとめる(別々に呼ぶより約25%速い)。長いときは2本を並行に処理する。
func ConvolvePair(ctx context.Context, x, irA, irB []float32) ([]float32, []float32, error) {
	if len(x) > 0 && len(irA) > 0 && len(irB) > 0 && len(irA) <= shortIRMax && len(irB) <= shortIRMax {
		out, err := runStream(ctx, NewStreamConvolver(irA, irB), x, [][]float32{irA, irB})
		if err != nil {
			return nil, nil, err
		}
		return out[0], out[1], nil
	}
	var a, b []float32
	var errA, errB error
	done := make(chan struct{})
	go func() {
		defer close(done)
		b, errB = Convolve(ctx, x, irB)
	}()
	a, errA = Convolve(ctx, x, irA)
	<-done
	if errA != nil {
		return nil, nil, errA
	}
	return a, b, errB
}

// runStream は x を c に streamChunk ずつ流し、IRごとの全出力(長さ len(x)+len(ir)-1)を集める。
func runStream(ctx context.Context, c *StreamConvolver, x []float32, irs [][]float32) ([][]float32, error) {
	outs := make([][]float32, len(irs))
	for k, ir := range irs {
		outs[k] = make([]float32, 0, len(x)+len(ir)-1)
	}
	for from := 0; from < len(x); from += streamChunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for k, y := range c.Process(x[from:min(from+streamChunk, len(x))]) {
			outs[k] = append(outs[k], y...)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for k, y := range c.Flush() {
		outs[k] = append(outs[k], y...)
	}
	return outs, nil
}

// StreamConvolver は、入力を区切って順に与えながら、1つまたは複数のIRとの線形畳み込みを求める(overlap-save)。
// 結果は、入力全体を一度に畳み込んだ場合と、区切り方に依らずビット単位で一致する
// (ブロックの分け方は、曲頭からの絶対位置だけで決まる)。
//
// 全IRが shortIRMax 以下なら、入力のFFTを共有して1回のFFTで処理する(短IR用。FFT長 shortFFTSize、
// 1回に N-L+1 サンプルを出す。L は最長のIR長)。そうでなければIRごとに、IR長から決めた分割サイズの
// 周波数領域遅延線で処理する(IRが短いものは、そのIRだけの短IR用)。
type StreamConvolver struct {
	irLens []int
	n, b   int       // FFT長、1回のブロックで出す(確定する)サンプル数
	buf    []float32 // 次のブロックに要る入力(絶対位置 blocks*b - lead から)。先頭の lead 個は曲頭より前のゼロ
	off    int
	blocks int // 処理したブロック数
	fed    int // 受け取った入力のフレーム数
	block  func(frame []float32) [][]float32
	out    [][]float32
	subs   []*StreamConvolver // IRごとに分ける場合
}

// NewStreamConvolver は irs との畳み込みを流し処理する畳み込み器を返す。IRは空でないこと。
func NewStreamConvolver(irs ...[]float32) *StreamConvolver {
	allShort := true
	for _, ir := range irs {
		if len(ir) > shortIRMax {
			allShort = false
		}
	}
	if allShort {
		return newShortStream(irs)
	}
	c := &StreamConvolver{irLens: irLens(irs), out: make([][]float32, len(irs))}
	for _, ir := range irs {
		if len(ir) <= shortIRMax {
			c.subs = append(c.subs, newShortStream([][]float32{ir}))
		} else {
			c.subs = append(c.subs, newPartitionedStream(ir, partitionSize(len(ir))))
		}
	}
	return c
}

func irLens(irs [][]float32) []int {
	out := make([]int, len(irs))
	for i, ir := range irs {
		out[i] = len(ir)
	}
	return out
}

// newShortStream は短いIR用(入力のFFTを共有)。
func newShortStream(irs [][]float32) *StreamConvolver {
	L := 0
	for _, ir := range irs {
		L = max(L, len(ir))
	}
	N := shortFFTSize
	B := N - L + 1
	fft := fourier.NewFFT(N)
	nc := N/2 + 1
	buf := make([]float64, N)
	H := make([][]complex128, len(irs))
	for k, ir := range irs {
		for i := range buf {
			buf[i] = 0
		}
		for i := range ir {
			buf[i] = float64(ir[i])
		}
		H[k] = fft.Coefficients(make([]complex128, nc), buf)
	}
	X := make([]complex128, nc)
	Y := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)
	res := make([][]float32, len(irs))
	for k := range res {
		res[k] = make([]float32, B)
	}
	return &StreamConvolver{
		irLens: irLens(irs), n: N, b: B, buf: make([]float32, L-1), out: make([][]float32, len(irs)),
		block: func(frame []float32) [][]float32 {
			// フレームは x[start-(L-1) .. start-(L-1)+N)。先頭の L-1 点は巡回で汚れるので捨てる
			for i := 0; i < N; i++ {
				buf[i] = float64(frame[i])
			}
			fft.Coefficients(X, buf)
			for k := range irs {
				for i := range Y {
					Y[i] = X[i] * H[k][i]
				}
				fft.Sequence(y, Y)
				for i := 0; i < B; i++ {
					res[k][i] = float32(y[L-1+i] * scale)
				}
			}
			return res
		},
	}
}

// newPartitionedStream は IR を B 点ずつに分け、FFT長 2B の周波数領域遅延線で畳み込む(長いIR用)。
func newPartitionedStream(ir []float32, B int) *StreamConvolver {
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

	// 周波数領域の遅延線(新しい入力スペクトルほど小さい添字)
	fdl := make([][]complex128, parts)
	for i := range fdl {
		fdl[i] = make([]complex128, nc)
	}
	acc := make([]complex128, nc)
	y := make([]float64, N)
	scale := 1.0 / float64(N)
	res := [][]float32{make([]float32, B)}
	return &StreamConvolver{
		irLens: []int{len(ir)}, n: N, b: B, buf: make([]float32, B), out: make([][]float32, 1),
		block: func(frame []float32) [][]float32 {
			// 入力フレーム = [前のブロック, 今のブロック]
			for i := 0; i < N; i++ {
				buf[i] = float64(frame[i])
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
				res[0][i] = float32(y[B+i] * scale)
			}
			return res
		},
	}
}

// Process は入力 x の続きを与え、新しく確定した出力をIRごとに返す。戻り値は次の Process / Flush まで有効。
// 全IRが短いとき(またはIRが1つのとき)、確定する出力の長さはIRによらず同じ。
func (c *StreamConvolver) Process(x []float32) [][]float32 {
	if c.subs != nil {
		for k, s := range c.subs {
			c.out[k] = s.Process(x)[0]
		}
		return c.out
	}
	for k := range c.out {
		c.out[k] = c.out[k][:0]
	}
	c.buf = append(c.buf, x...)
	c.fed += len(x)
	for len(c.buf)-c.off >= c.n {
		ys := c.block(c.buf[c.off : c.off+c.n])
		for k := range c.out {
			c.out[k] = append(c.out[k], ys[k]...)
		}
		c.off += c.b
		c.blocks++
	}
	c.buf = c.buf[:copy(c.buf, c.buf[c.off:])]
	c.off = 0
	return c.out
}

// Flush は入力の終わりを知らせ、残りの出力(尾)を返す。戻り値は Process と同じく次の呼び出しまで有効。
// Process と Flush の出力を合わせた長さは、IR k について 入力の長さ + len(irs[k]) - 1(入力が0なら空)。
func (c *StreamConvolver) Flush() [][]float32 {
	if c.subs != nil {
		for k, s := range c.subs {
			c.out[k] = s.Flush()[0]
		}
		return c.out
	}
	for k := range c.out {
		c.out[k] = c.out[k][:0]
	}
	if c.fed == 0 {
		return c.out
	}
	maxOut := 0
	for _, l := range c.irLens {
		maxOut = max(maxOut, c.fed+l-1)
	}
	total := (maxOut + c.b - 1) / c.b
	for c.blocks < total {
		for len(c.buf)-c.off < c.n {
			c.buf = append(c.buf, 0)
		}
		ys := c.block(c.buf[c.off : c.off+c.n])
		start := c.blocks * c.b
		for k := range c.out {
			if take := min(c.b, c.fed+c.irLens[k]-1-start); take > 0 {
				c.out[k] = append(c.out[k], ys[k][:take]...)
			}
		}
		c.off += c.b
		c.blocks++
	}
	c.buf = c.buf[:0]
	c.off = 0
	return c.out
}
