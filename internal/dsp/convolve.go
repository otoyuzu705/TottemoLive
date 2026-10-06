package dsp

import (
	"context"
	"runtime"

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

// ConvolvePair は同じ入力 x を2つのIR(左右の耳など)で畳み込む。IRが両方とも短いとき、または
// 両方とも長くて分割サイズが同じときは、入力のFFT(長いときは周波数領域遅延線ごと)を1つにまとめる。
// それ以外(長さの混在など)は2本を並行に処理する。
func ConvolvePair(ctx context.Context, x, irA, irB []float32) ([]float32, []float32, error) {
	shared := len(irA) > 0 && len(irB) > 0 &&
		((len(irA) <= shortIRMax && len(irB) <= shortIRMax) ||
			(len(irA) > shortIRMax && len(irB) > shortIRMax && partitionSize(len(irA)) == partitionSize(len(irB))))
	if len(x) > 0 && shared {
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
// 結果は、入力全体を一度に畳み込んだ場合と、区切り方・並列度に依らずビット単位で一致する
// (ブロックの分け方は、曲頭からの絶対位置だけで決まる)。
//
// 構成は次の3通り。
//   - 全IRが shortIRMax 以下: 入力のFFTを共有して1回のFFTで処理する(短IR用。FFT長 shortFFTSize、
//     1回に N-L+1 サンプルを出す。L は最長のIR長)
//   - 全IRが長く、分割サイズが同じ(会場IRの左右など): IR長から決めた分割サイズの周波数領域遅延線を
//     IR間で共有して処理する(遅延線が持つのは入力のスペクトルだけで、IRのスペクトルは別々に持つ)
//   - それ以外(長さの混在・分割サイズの違い): IRごとに別々の畳み込み器(subs)に分ける
//
// 1回の Process に入るブロックは互いに独立なので、ブロックを並列に処理する(結果は並列度に依らない)。
type StreamConvolver struct {
	irLens []int
	n, b   int       // FFT長、1回のブロックで出す(確定する)サンプル数
	buf    []float32 // 次のブロックに要る入力(絶対位置 blocks*b - lead から)。先頭の lead 個は曲頭より前のゼロ
	off    int
	blocks int // 処理したブロック数
	fed    int // 受け取った入力のフレーム数
	// run は、frames の先頭から b ずつずらした k 個のフレーム(それぞれ n 点)を処理し、
	// ブロック j の出力(b 点)を IR r について dst[r][j*b : (j+1)*b] に書く。frames は読むだけ。
	run  func(frames []float32, k int, dst [][]float32)
	out  [][]float32
	subs []*StreamConvolver // IRごとに分ける場合
}

// NewStreamConvolver は irs との畳み込みを流し処理する畳み込み器を返す。IRは空でないこと。
func NewStreamConvolver(irs ...[]float32) *StreamConvolver {
	allShort, allLong := true, true
	for _, ir := range irs {
		if len(ir) > shortIRMax {
			allShort = false
		} else {
			allLong = false
		}
	}
	if allShort {
		return newShortStream(irs)
	}
	if allLong {
		B := partitionSize(len(irs[0]))
		same := true
		for _, ir := range irs {
			same = same && partitionSize(len(ir)) == B
		}
		if same {
			return newPartitionedStream(irs, B)
		}
	}
	c := &StreamConvolver{irLens: irLens(irs), out: make([][]float32, len(irs))}
	for _, ir := range irs {
		if len(ir) <= shortIRMax {
			c.subs = append(c.subs, newShortStream([][]float32{ir}))
		} else {
			c.subs = append(c.subs, newPartitionedStream([][]float32{ir}, partitionSize(len(ir))))
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

// shortWorker は、短いIR用のブロックを処理するワーカーごとの作業領域(FFTインスタンスはワーカーごと。
// partWorker と同じ理由)。
type shortWorker struct {
	fft  *fourier.FFT
	buf  []float64
	y    []float64
	X, Y []complex128
}

// shortConv は短いIR用(入力のFFTを共有)。overlap-save のブロックは完全に独立なので、
// 1回の Process に入るブロック(65536フレームで16〜19個)をワーカーに分けて並列に処理する。
type shortConv struct {
	N, L, B, nc int
	H           [][]complex128 // [IR]
	scale       float64
	workers     []*shortWorker
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
	sc := &shortConv{N: N, L: L, B: B, nc: nc, scale: 1.0 / float64(N), H: make([][]complex128, len(irs))}
	for k, ir := range irs {
		for i := range buf {
			buf[i] = 0
		}
		for i := range ir {
			buf[i] = float64(ir[i])
		}
		sc.H[k] = fft.Coefficients(make([]complex128, nc), buf)
	}
	return &StreamConvolver{
		irLens: irLens(irs), n: N, b: B, buf: make([]float32, L-1), out: make([][]float32, len(irs)),
		run: sc.run,
	}
}

// run は k 個のブロックを処理する(StreamConvolver.run を見よ)。フレームは読み取り専用で、
// ブロック j の出力は dst の j*B の位置へ書くので、ブロックごとに独立に並列化できる。
func (s *shortConv) run(frames []float32, k int, dst [][]float32) {
	nw := min(runtime.GOMAXPROCS(0), k)
	for len(s.workers) < nw {
		s.workers = append(s.workers, &shortWorker{
			fft: fourier.NewFFT(s.N), buf: make([]float64, s.N), y: make([]float64, s.N),
			X: make([]complex128, s.nc), Y: make([]complex128, s.nc),
		})
	}
	N, L, B := s.N, s.L, s.B
	parallelDo(nw, func(w int) {
		wk := s.workers[w]
		for j := k * w / nw; j < k*(w+1)/nw; j++ {
			// フレームは x[start-(L-1) .. start-(L-1)+N)。先頭の L-1 点は巡回で汚れるので捨てる
			frame := frames[j*B : j*B+N]
			for i := 0; i < N; i++ {
				wk.buf[i] = float64(frame[i])
			}
			wk.fft.Coefficients(wk.X, wk.buf)
			for r, h := range s.H {
				for i := range wk.Y {
					wk.Y[i] = wk.X[i] * h[i]
				}
				wk.fft.Sequence(wk.y, wk.Y)
				out := dst[r][j*B : (j+1)*B]
				for i := range out {
					out[i] = float32(wk.y[L-1+i] * s.scale)
				}
			}
		}
	})
}

// partWorker は、分割畳み込みのブロックを処理するワーカーごとの作業領域。gonum の FFT は作業領域を
// 書き換えるのでゴルーチン安全ではなく、ワーカーごとに別のインスタンスを持つ(回転因子は決定的なので、
// インスタンスが違っても結果は同じ)。
type partWorker struct {
	fft *fourier.FFT
	buf []float64
	y   []float64
	acc []complex128
}

// partConv は、IR を B 点ずつに分け、FFT長 2B の周波数領域遅延線(FDL)で畳み込む(長いIR用)。
// FDL は入力のスペクトルだけを持つので、同じ分割サイズの複数のIR(左右の耳など)で1つを共有し、
// IRごとに持つのはIRのスペクトル H だけ。
type partConv struct {
	B, N, nc int
	H        [][][]complex128 // [IR][分割 p]
	// fdl[q] は、直前までの入力の、q+1 個前のブロックのスペクトル(q=0 が直前のブロック)。初期値はゼロ
	fdl     [][]complex128
	spare   [][]complex128 // 今回の入力ブロックのスペクトルに使う配列(使い回す。必要になってから増やす)
	order   [][]complex128 // FDL を進めるときの付け替え用の作業(ポインタだけ)
	spare2  [][]complex128
	workers []*partWorker
	scale   float64
}

// newPartitionedStream は irs(分割サイズ B は共通)を FDL を共有して畳み込む畳み込み器を返す。
func newPartitionedStream(irs [][]float32, B int) *StreamConvolver {
	N := 2 * B
	nc := N/2 + 1
	fft := fourier.NewFFT(N)
	pc := &partConv{B: B, N: N, nc: nc, scale: 1.0 / float64(N), H: make([][][]complex128, len(irs))}
	buf := make([]float64, N)
	maxParts := 0
	for k, ir := range irs {
		// IRをB点ずつに分けてスペクトルにしておく
		parts := (len(ir) + B - 1) / B
		maxParts = max(maxParts, parts)
		pc.H[k] = make([][]complex128, parts)
		for p := 0; p < parts; p++ {
			for i := range buf {
				buf[i] = 0
			}
			for i := 0; i < B && p*B+i < len(ir); i++ {
				buf[i] = float64(ir[p*B+i])
			}
			pc.H[k][p] = fft.Coefficients(make([]complex128, nc), buf)
		}
	}
	pc.fdl = make([][]complex128, maxParts)
	for q := range pc.fdl {
		pc.fdl[q] = make([]complex128, nc)
	}
	return &StreamConvolver{
		irLens: irLens(irs), n: N, b: B, buf: make([]float32, B), out: make([][]float32, len(irs)),
		run: pc.run,
	}
}

// ensureWorkers は、ワーカー 0..n-1 の作業領域を(無ければ)作る。ゴルーチンを起こす前に呼ぶ。
func (p *partConv) ensureWorkers(n int) {
	for len(p.workers) < n {
		p.workers = append(p.workers, &partWorker{
			fft: fourier.NewFFT(p.N), buf: make([]float64, p.N), y: make([]float64, p.N), acc: make([]complex128, p.nc),
		})
	}
}

// run は k 個のブロックを処理する(StreamConvolver.run を見よ)。
//
//	(1) 全ブロックの順方向FFT(ブロックごとに独立 → 並列)
//	(2) ブロック × IR ごとの FDL 積和と逆FFT(独立 → 並列)。ブロック j の積和が使うのは「ブロック j-q のスペクトル」
//	    (q=0 が最新)で、j-q が負なら前回までの FDL の fdl[q-j-1]。積和は q=0.. の順(元の逐次処理と同じ順)
//	(3) FDL を k ブロックぶん進める(配列の付け替えだけで、データは動かさない)
func (p *partConv) run(frames []float32, k int, dst [][]float32) {
	B, N, nc, nIR := p.B, p.N, p.nc, len(p.H)
	for len(p.spare) < k {
		p.spare = append(p.spare, make([]complex128, nc))
	}
	X := p.spare[:k]

	nw := min(runtime.GOMAXPROCS(0), k)
	p.ensureWorkers(nw)
	parallelDo(nw, func(w int) {
		wk := p.workers[w]
		for j := k * w / nw; j < k*(w+1)/nw; j++ {
			fr := frames[j*B : j*B+N]
			for i := range wk.buf {
				wk.buf[i] = float64(fr[i])
			}
			wk.fft.Coefficients(X[j], wk.buf)
		}
	})

	tasks := k * nIR
	nw = min(runtime.GOMAXPROCS(0), tasks)
	p.ensureWorkers(nw)
	parallelDo(nw, func(w int) {
		wk := p.workers[w]
		for t := tasks * w / nw; t < tasks*(w+1)/nw; t++ {
			j, r := t/nIR, t%nIR
			acc := wk.acc
			for i := range acc {
				acc[i] = 0
			}
			for q, h := range p.H[r] {
				var a []complex128
				if j-q >= 0 {
					a = X[j-q]
				} else {
					a = p.fdl[q-j-1]
				}
				for i := 0; i < nc; i++ {
					acc[i] += a[i] * h[i]
				}
			}
			wk.fft.Sequence(wk.y, acc)
			out := dst[r][j*B : (j+1)*B]
			for i := range out {
				out[i] = float32(wk.y[B+i] * p.scale)
			}
		}
	})

	// FDL を進める: 新しい順に X[k-1] ... X[0]、続いて古い FDL。先頭の len(fdl) 個が新しい FDL、残りは使い回しの配列
	order := p.order[:0]
	for j := k - 1; j >= 0; j-- {
		order = append(order, X[j])
	}
	order = append(order, p.fdl...)
	copy(p.fdl, order[:len(p.fdl)])
	spare2 := append(p.spare2[:0], order[len(p.fdl):]...)
	spare2 = append(spare2, p.spare[k:]...)
	p.spare, p.spare2 = spare2, p.spare
	p.order = order
}

// Process は入力 x の続きを与え、新しく確定した出力をIRごとに返す。戻り値は次の Process / Flush まで有効。
// 確定する出力の長さは、全IRで同じ(全IRが短い・全IRが長くて分割サイズが同じ・IRが1つ のとき。
// それ以外で IRごとの畳み込み器(subs)に分かれるときは、分割サイズの違いでIRごとに違うことがある)。
func (c *StreamConvolver) Process(x []float32) [][]float32 {
	if c.subs != nil {
		for k, s := range c.subs {
			c.out[k] = s.Process(x)[0]
		}
		return c.out
	}
	c.buf = append(c.buf, x...)
	c.fed += len(x)
	k := 0
	if avail := len(c.buf) - c.off; avail >= c.n {
		k = (avail-c.n)/c.b + 1
	}
	c.runBlocks(k)
	c.off += k * c.b
	c.blocks += k
	c.buf = c.buf[:copy(c.buf, c.buf[c.off:])]
	c.off = 0
	return c.out
}

// runBlocks は c.buf[c.off:] から k ブロックを処理し、c.out に(各IR k*b 点)出す。
func (c *StreamConvolver) runBlocks(k int) {
	for r := range c.out {
		if cap(c.out[r]) < k*c.b {
			c.out[r] = make([]float32, k*c.b)
		}
		c.out[r] = c.out[r][:k*c.b]
	}
	if k > 0 {
		c.run(c.buf[c.off:], k, c.out)
	}
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
	if kk := total - c.blocks; kk > 0 {
		// 残りのブロックは、入力の終わりの先をゼロとして処理する
		if need := c.off + (kk-1)*c.b + c.n; len(c.buf) < need {
			c.buf = append(c.buf, make([]float32, need-len(c.buf))...)
		}
		c.runBlocks(kk)
		start := c.blocks * c.b
		for r := range c.out {
			c.out[r] = c.out[r][:max(0, min(kk*c.b, c.fed+c.irLens[r]-1-start))]
		}
		c.blocks += kk
	}
	c.buf = c.buf[:0]
	c.off = 0
	return c.out
}
