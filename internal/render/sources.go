package render

import (
	"context"
	"errors"
	"io"
	"sync"

	"tottemolive/internal/audio"
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

// paProc は音源1本ぶんの(ゲイン → PA質感)の処理器。フィルタ・コンプの状態を持つので、
// 音源を区切って順に Process してよい(結果は区切り方に依らない)。
type paProc struct {
	g     float32
	f     [2][3]*dsp.Biquad // チャンネルごとの 低域カット → 低域シェルフ → 高域シェルフ
	comp  *dsp.Compressor
	drive float64
}

// newPAProc は、音源ゲイン(+レベル合わせ)gainDb と pa.* の設定の処理器を返す。
func newPAProc(sr int, gainDb float64, pa project.PA) *paProc {
	p := &paProc{
		g: float32(dsp.DbToLin(gainDb)),
		comp: dsp.NewCompressor(sr, dsp.CompParams{
			ThresholdDb: pa.CompThresholdDb, Ratio: pa.CompRatio,
			AttackMs: pa.CompAttackMs, ReleaseMs: pa.CompReleaseMs,
		}),
		drive: pa.Drive,
	}
	for c := range p.f {
		p.f[c] = [3]*dsp.Biquad{
			dsp.HighPass(float64(sr), pa.LowCutHz),
			dsp.LowShelf(float64(sr), pa.LowShelfHz, pa.LowShelfDb),
			dsp.HighShelf(float64(sr), pa.HighShelfHz, pa.HighShelfDb),
		}
	}
	return p
}

// Process は buf(ステレオ)にその場でゲインとPA質感(低域カット → 低域シェルフ → 高域シェルフ → コンプ → 歪み)を掛ける。
func (p *paProc) Process(buf [][]float32) {
	for c, ch := range buf {
		for i := range ch {
			ch[i] *= p.g
		}
		for _, f := range p.f[c] {
			f.Process(ch)
		}
	}
	p.comp.Process(buf)
	dsp.Saturate(buf, p.drive)
}

// applyPA は音源にゲインを掛けてPA質感(低域カット → 低域シェルフ → 高域シェルフ → コンプ → 歪み)を付ける。
// paProc に全体を渡す包み。
func applyPA(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	newPAProc(sr, gainDb, pa).Process(buf)
}

// liveBus は音源のデコーダーから、(音源ごとのゲイン・PA質感 → 音源の順に足し合わせ)を流しながら、バスを少しずつ読み出す。
// 音源ごとの処理(デコード・PA)は並列に回し、足し合わせは音源の順(0 + 音源0 + 音源1 + ...)で固定する。
// 音源は長さが違ってよい。終わった音源は、それ以降は寄与しない(無音として足さない)。
type liveBus struct {
	decs   []frameReader
	pa     []*paProc // 音源ごとのPA処理(nil ならPAなし = レベル測定・原音)
	gains  []float32 // PAなしのときの、音源ごとのゲイン(線形)
	done   []bool
	frames int // 読み出したバスのフレーム数
	bufs   [][][]float32
	n      []int
	errs   []error
}

// newLiveBus は decs を音源の順に足し合わせるバスを返す。pa が nil でなければ、音源ごとに pa[i] を通す
// (ゲインは pa[i] が持つ)。nil なら gains[i] を掛けるだけ。chunk は1回に読むフレーム数の上限。
func newLiveBus(decs []frameReader, pa []*paProc, gains []float32, chunk int) *liveBus {
	b := &liveBus{decs: decs, pa: pa, gains: gains, done: make([]bool, len(decs)),
		bufs: make([][][]float32, len(decs)), n: make([]int, len(decs)), errs: make([]error, len(decs))}
	for i := range b.bufs {
		b.bufs[i] = [][]float32{make([]float32, chunk), make([]float32, chunk)}
	}
	return b
}

// Read は dst(ステレオ)を最大まで読み、読めたフレーム数を返す。全音源が終わっていれば 0, io.EOF。
func (b *liveBus) Read(dst [][]float32) (int, error) {
	want := len(dst[0])
	var wg sync.WaitGroup
	for i := range b.decs {
		b.n[i], b.errs[i] = 0, nil
		if b.done[i] {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := [][]float32{b.bufs[i][0][:want], b.bufs[i][1][:want]}
			n, err := b.decs[i].Read(buf)
			if err == io.EOF {
				b.done[i] = true
				err = nil
			}
			b.errs[i] = err
			if err != nil || n == 0 {
				return
			}
			buf = [][]float32{buf[0][:n], buf[1][:n]}
			if b.pa != nil {
				b.pa[i].Process(buf)
			} else {
				for _, ch := range buf {
					for k := range ch {
						ch[k] *= b.gains[i]
					}
				}
			}
			b.n[i] = n
		}()
	}
	wg.Wait()
	if err := errors.Join(b.errs...); err != nil {
		return 0, err
	}
	n := 0
	for _, k := range b.n {
		n = max(n, k)
	}
	if n == 0 {
		return 0, io.EOF
	}
	for c := range dst {
		clear(dst[c][:n])
		for i := range b.decs {
			for k, v := range b.bufs[i][c][:b.n[i]] {
				dst[c][k] += v
			}
		}
	}
	b.frames += n
	return n, nil
}

// close は全デコーダーを閉じる。
func (b *liveBus) close() {
	for _, d := range b.decs {
		d.Close()
	}
}

// decodeProgress は、音源ごとのデコードの進み具合を平均して、デコード段階の進捗として通知する。
type decodeProgress struct {
	mu      sync.Mutex
	ratios  []float64
	prog    Progress
	touched bool // 進捗を通知した(デコードが行われた)
}

// used は、進捗を一度でも通知したか(この進捗でデコード段階を報告済みか)。
func (d *decodeProgress) used() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.touched
}

func newDecodeProgress(prog Progress, n int) *decodeProgress {
	return &decodeProgress{ratios: make([]float64, n), prog: prog}
}

// fn は音源 i のデコーダーに渡す進捗通知の関数。
func (d *decodeProgress) fn(i int) func(float64) {
	if d == nil || d.prog == nil {
		return nil
	}
	return func(r float64) {
		d.mu.Lock()
		d.touched = true
		d.ratios[i] = r
		sum := 0.0
		for _, v := range d.ratios {
			sum += v
		}
		d.mu.Unlock()
		d.prog.report(StageDecode, sum/float64(len(d.ratios)))
	}
}

// openSources は全音源のデコーダーを開く(失敗したら、開いたものを閉じてエラー)。
func (e *Engine) openSources(ctx context.Context, sources []project.Source, dp *decodeProgress) ([]frameReader, error) {
	decs := make([]frameReader, 0, len(sources))
	for i, s := range sources {
		e.decodes.Add(1)
		var d frameReader
		var err error
		if e.openSource != nil {
			d, err = e.openSource(ctx, s.Path, sampleRate, dp.fn(i))
		} else {
			d, err = audio.OpenDecoder(ctx, s.Path, sampleRate, dp.fn(i))
		}
		if err != nil {
			for _, o := range decs {
				o.Close()
			}
			return nil, err
		}
		decs = append(decs, d)
	}
	return decs, nil
}

// expectedFrames は、デコーダーの総フレーム数の見積もりのうち最大のもの(進捗の分母。分からなければ 0)。
func expectedFrames(decs []frameReader) int {
	n := 0
	for _, d := range decs {
		n = max(n, d.ExpectedFrames())
	}
	return n
}

// measureInputLevel は、音源ゲイン後の合計(バス)の統合ラウドネスを、音源を流しながら測る。
// 読むもの: 音源のファイルとゲインだけ。
func (e *Engine) measureInputLevel(ctx context.Context, p project.Project, dp *decodeProgress) (float64, error) {
	decs, err := e.openSources(ctx, p.Sources, dp)
	if err != nil {
		return 0, err
	}
	gains := make([]float32, len(p.Sources))
	for i, s := range p.Sources {
		gains[i] = float32(dsp.DbToLin(s.GainDb))
	}
	bus := newLiveBus(decs, nil, gains, e.chunkSize())
	defer bus.close()
	meter := dsp.NewLoudnessMeter(sampleRate, 2)
	buf := [][]float32{make([]float32, e.chunkSize()), make([]float32, e.chunkSize())}
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, err := bus.Read(buf)
		if err == io.EOF {
			return meter.Integrated(), nil
		}
		if err != nil {
			return 0, err
		}
		meter.Write([][]float32{buf[0][:n], buf[1][:n]})
	}
}
