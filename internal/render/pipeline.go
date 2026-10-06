package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"tottemolive/internal/analysis"
	"tottemolive/internal/audio"
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
	"tottemolive/internal/venue"
)

// Render はプロジェクト全体をレンダリングする(キャッシュなし)。結果の音声をメモリに持つので、
// テストと短い素材用。長い曲は Export か RenderTo(Sink)を使う。
func Render(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	return (&Engine{}).renderMem(ctx, p, prog)
}

// renderMem は RenderTo の結果をメモリに集める。
func (e *Engine) renderMem(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	sink := &memSink{}
	res, err := e.RenderTo(ctx, p, prog, sink)
	if err != nil {
		return nil, err
	}
	res.Audio = sink.Audio
	return res, nil
}

// Export はレンダリングして24bit WAVに書き出す。
func Export(ctx context.Context, p project.Project, outPath string, prog Progress) error {
	sink := &wavSink{ctx: ctx, path: outPath}
	if _, err := (&Engine{}).RenderTo(ctx, p, prog, sink); err != nil {
		sink.abort()
		return err
	}
	return sink.close()
}

// wavSink は出力を24bit WAVファイルへ流す Sink。
type wavSink struct {
	ctx  context.Context
	path string
	enc  *audio.WAVEncoder
}

func (w *wavSink) Start(frames, sampleRate int) error {
	enc, err := audio.NewWAVEncoder(w.ctx, w.path, sampleRate)
	w.enc = enc
	return err
}

func (w *wavSink) Write(buf [][]float32) error { return w.enc.Write(buf) }

func (w *wavSink) abort() {
	if w.enc != nil {
		w.enc.Abort()
	}
}

func (w *wavSink) close() error {
	if w.enc == nil {
		return errors.New("render: 出力を始められませんでした")
	}
	return w.enc.Close()
}

// RenderTo はプロジェクト全体を、音源を流しながらレンダリングして sink へ書き出す。
// 曲の長さに依らず、メモリはほぼ一定(段の出力は一時ファイルに置く)。
func (e *Engine) RenderTo(ctx context.Context, p project.Project, prog Progress, sink Sink) (*Result, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	p = pp.p
	dir, cleanup, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	// パス0: PA入力のレベル合わせのために、音源ゲイン後の合計のラウドネスを測る
	dp := newDecodeProgress(prog, len(p.Sources))
	alignDb := 0.0
	if p.PA.AutoLevel == "on" {
		lufs, err := e.measureInputLevel(ctx, p, dp)
		if err != nil {
			return nil, err
		}
		if !math.IsInf(lufs, 0) && !math.IsNaN(lufs) {
			alignDb = math.Min(math.Max(p.PA.InputLufs-lufs, -maxAlignDb), maxAlignDb)
		}
		dp = nil // デコードの進捗は、測定のほうで通知した
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// パス1: 音源 → PA → バス → 直接音・残響・(スペクトラム) → ミックス(一時ファイルへ)
	prog.report(StageProcess, 0)
	decs, err := e.openSources(ctx, p.Sources, dp)
	if err != nil {
		return nil, err
	}
	pas := make([]*paProc, len(p.Sources))
	for i, s := range p.Sources {
		pas[i] = newPAProc(sampleRate, s.GainDb+alignDb, p.PA)
	}
	bus := newLiveBus(decs, pas, nil, e.chunkSize())
	defer bus.close()
	expected := expectedFrames(decs)

	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	// 残響は直接音より先に届かない: 最初に届く音(いちばん近いメインスピーカーの直接音)から残響が始まる。
	// プリディレイはそこからの遅れになる
	revDelay := firstArrivalSamples(p)
	dproc, rproc := newDirectProc(pp), newReverbProc(pp, ir)
	var an *analysis.Analyzer
	var paMS analysis.MeanSquareAcc
	if e.analyzePA {
		an = analysis.NewAnalyzer(sampleRate)
	}

	mw, err := newSpoolWriter(dir, 2)
	if err != nil {
		return nil, err
	}
	mixMeter := dsp.NewLoudnessMeter(sampleRate, 2)
	mx := &mixer{g: mixGainsFor(p, pp.pr), revDelay: revDelay, d: dproc.out, r: rproc.out,
		emit: func(buf [][]float32) error {
			mixMeter.Write(buf)
			return mw.Write(buf)
		}}

	buf := [][]float32{make([]float32, e.chunkSize()), make([]float32, e.chunkSize())}
	for {
		if err := ctx.Err(); err != nil {
			mw.Abort()
			return nil, err
		}
		n, err := bus.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			mw.Abort()
			return nil, err
		}
		chunk := [][]float32{buf[0][:n], buf[1][:n]}
		var aerr error
		var wg sync.WaitGroup
		wg.Add(3)
		go func() { defer wg.Done(); dproc.Push(chunk) }()
		go func() { defer wg.Done(); rproc.Push(chunk) }()
		go func() {
			defer wg.Done()
			if an != nil {
				mono := analysis.Mono(chunk[0], chunk[1])
				paMS.Add(mono)
				aerr = an.Write(ctx, mono)
			}
		}()
		wg.Wait()
		if aerr == nil {
			aerr = mx.drain(math.MaxInt)
		}
		if aerr != nil {
			mw.Abort()
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, aerr
		}
		if expected > 0 {
			prog.report(StageProcess, float64(bus.frames)/float64(expected))
		}
	}
	songLen := bus.frames
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); dproc.Flush() }()
	go func() { defer wg.Done(); rproc.Flush() }()
	wg.Wait()
	outLen := songLen + revDelay + len(ir[0])
	if err := mx.drain(outLen); err != nil {
		mw.Abort()
		return nil, err
	}
	if mx.pos != outLen {
		mw.Abort()
		return nil, fmt.Errorf("render: ミックスが %d フレームのはずが %d フレームでした", outLen, mx.pos)
	}
	var series *analysis.Series
	if an != nil {
		if series, err = an.Finish(ctx); err != nil {
			mw.Abort()
			return nil, err
		}
	}
	mixSp, err := mw.Commit(spoolMeta{SongLen: songLen})
	if err != nil {
		return nil, err
	}
	defer mixSp.release()
	prog.report(StageProcess, 1)

	// パス2: マスター(ラウドネス調整 → リミッタ)を掛けて sink へ
	outLufs, finalMS, err := masterPass(ctx, mixSp, outLen, songLen, mixMeter.Integrated(), p.Output, sink, prog)
	if err != nil {
		return nil, err
	}
	res := &Result{Frames: outLen, SampleRate: sampleRate, LUFS: outLufs}
	if series != nil {
		res.PA = &PASpectrum{Series: series, OffsetDb: levelOffsetFromMS(paMS.Value(), finalMS)}
	}
	return res, nil
}

// runDir は、この実行のスプールを置くディレクトリと、終わったときの後始末を返す。
// キャッシュなしのエンジン(書き出し)は、実行ごとに作って消す。キャッシュ付きは Engine の一時ディレクトリを使う。
func (e *Engine) runDir() (string, func(), error) {
	if e.cache != nil {
		d, err := e.tempDir()
		return d, func() {}, err
	}
	d, err := os.MkdirTemp("", renderTempPrefix+"*")
	if err != nil {
		return "", nil, fmt.Errorf("一時フォルダを作れません: %w", err)
	}
	return d, func() { os.RemoveAll(d) }, nil
}

// Original は曲全体の原音(音源にゲインを掛けて足しただけ)を返す。A/B比較用。
// 結果の音声をメモリに持つので、テストと短い素材用。
func (e *Engine) Original(ctx context.Context, p project.Project) (*Result, error) {
	sink := &memSink{}
	res, err := e.OriginalTo(ctx, p, sink)
	if err != nil {
		return nil, err
	}
	res.Audio = sink.Audio
	return res, nil
}

// OriginalTo は原音を sink へ書き出す。音量差で判断が偏らないよう、マスター(ラウドネス・リミッタ)だけは
// 通して同じ目標にそろえる。
func (e *Engine) OriginalTo(ctx context.Context, p project.Project, sink Sink) (*Result, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	p = pp.p
	dir, cleanup, err := e.runDir()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	decs, err := e.openSources(ctx, p.Sources, nil)
	if err != nil {
		return nil, err
	}
	gains := make([]float32, len(p.Sources))
	for i, s := range p.Sources {
		gains[i] = float32(math.Pow(10, s.GainDb/20))
	}
	bus := newLiveBus(decs, nil, gains, e.chunkSize())
	defer bus.close()
	mw, err := newSpoolWriter(dir, 2)
	if err != nil {
		return nil, err
	}
	meter := dsp.NewLoudnessMeter(sampleRate, 2)
	buf := [][]float32{make([]float32, e.chunkSize()), make([]float32, e.chunkSize())}
	for {
		if err := ctx.Err(); err != nil {
			mw.Abort()
			return nil, err
		}
		n, err := bus.Read(buf)
		if err == io.EOF {
			break
		}
		if err != nil {
			mw.Abort()
			return nil, err
		}
		chunk := [][]float32{buf[0][:n], buf[1][:n]}
		meter.Write(chunk)
		if err := mw.Write(chunk); err != nil {
			mw.Abort()
			return nil, err
		}
	}
	songLen := bus.frames
	sp, err := mw.Commit(spoolMeta{SongLen: songLen})
	if err != nil {
		return nil, err
	}
	defer sp.release()
	outLufs, _, err := masterPass(ctx, sp, songLen, songLen, meter.Integrated(), p.Output, sink, nil)
	if err != nil {
		return nil, err
	}
	return &Result{Frames: songLen, SampleRate: sampleRate, LUFS: outLufs}, nil
}
