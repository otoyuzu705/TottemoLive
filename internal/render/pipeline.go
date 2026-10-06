package render

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
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

// Export はレンダリングして24bit WAVに書き出す(使い捨てのスプールは OS の一時ディレクトリに置く)。
func Export(ctx context.Context, p project.Project, outPath string, prog Progress) error {
	return ExportIn(ctx, "", p, outPath, prog)
}

// ExportIn は Export の、使い捨てのスプールを置く場所(tempDir。空なら OS の一時ディレクトリ)を指定できる版。
//
// 出力は、同じフォルダの一時名(out.partial-<乱数>.wav)へ書き、最後まで成功したときだけ outPath へ
// 置き換える(os.Rename。Windows でも既存のファイルを置き換える)。失敗・中断のときは一時ファイルを消し、
// 既存の outPath には触らない。
func ExportIn(ctx context.Context, tempDir string, p project.Project, outPath string, prog Progress) error {
	return exportWith(ctx, &Engine{baseDir: tempDir}, p, outPath, prog)
}

// exportWith は ExportIn の本体(エンジンを差し替えられる。テストで合成音源を使う)。
func exportWith(ctx context.Context, e *Engine, p project.Project, outPath string, prog Progress) error {
	partial, err := partialPath(outPath)
	if err != nil {
		return err
	}
	sink := &wavSink{ctx: ctx, path: partial}
	if _, err := e.RenderTo(ctx, p, prog, sink); err != nil {
		sink.abort()
		os.Remove(partial)
		return err
	}
	if err := sink.close(); err != nil {
		os.Remove(partial)
		return err
	}
	if err := os.Rename(partial, outPath); err != nil {
		os.Remove(partial)
		return fmt.Errorf("書き出し先を置き換えられません: %w", err)
	}
	return nil
}

// partialPath は outPath と同じフォルダに、書き出し途中のファイルの名前(拡張子は outPath と同じ)を決めて、空のファイルを作る。
func partialPath(outPath string) (string, error) {
	ext := filepath.Ext(outPath)
	base := strings.TrimSuffix(filepath.Base(outPath), ext)
	f, err := os.CreateTemp(filepath.Dir(outPath), base+".partial-*"+ext)
	if err != nil {
		return "", fmt.Errorf("書き出し先に書けません: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
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

// busReader はバスを区切って読み出すもの(音源を流す liveBus か、PA段のスプール)。
type busReader interface {
	// Read は dst を満たすまで読む。終わりは 0, io.EOF。
	Read(dst [][]float32) (int, error)
}

// spoolMixSrc は、キャッシュに当たった段(直接音・残響)のスプールを、ミックスの入力として順に読む。
// スプールは書き終えているので、入力はいつでもそろっている(終わりの先は無音)。読み出しの位置は、
// 順に増えていく前提(位置が飛んだら読み直す)。読み出しの失敗は err に残る。
type spoolMixSrc struct {
	s   *spool
	r   *spoolReader
	pos int
	err error
}

func (m *spoolMixSrc) availFrom(pos int) int { return math.MaxInt }

func (m *spoolMixSrc) readAt(pos int, dst [][]float32) {
	n := len(dst[0])
	for c := range dst {
		clear(dst[c])
	}
	skip := max(-pos, 0) // 曲頭より前は無音
	start := pos + skip
	cnt := min(n-skip, m.s.frames-start)
	if m.err != nil || cnt <= 0 {
		return
	}
	if m.r == nil || m.pos != start {
		if m.r != nil {
			m.r.Close()
			m.r = nil
		}
		r, err := m.s.open(start)
		if err != nil {
			m.err = err
			return
		}
		m.r = r
	}
	part := make([][]float32, len(dst))
	for c := range dst {
		part[c] = dst[c][skip : skip+cnt]
	}
	got, err := m.r.Read(part)
	if err != nil && err != io.EOF {
		m.err = err
	}
	m.pos = start + got
}

func (m *spoolMixSrc) close() {
	if m.r != nil {
		m.r.Close()
		m.r = nil
	}
}

// queueTee は、待ち行列に出てきた出力を、キャッシュ用のスプールにも書き写す。
// ミックスが先に読み進めて捨ててしまわないよう、ミックスの前に flush すること。
type queueTee struct {
	w   *spoolWriter
	pos int
}

func (t *queueTee) flush(q *frameQueue) error {
	n := q.produced - t.pos
	if t.w == nil || n <= 0 {
		return nil
	}
	buf := make([][]float32, len(q.ch))
	for c := range buf {
		buf[c] = make([]float32, n)
	}
	q.readAt(t.pos, buf)
	t.pos = q.produced
	return t.w.Write(buf)
}

// RenderTo は renderTo を呼ぶ。キャッシュに当たったスプールのファイルが(他から)消されていて読めなかったときは、
// そのスプールをキャッシュから外し(errSpoolLost で lost になる)、sink へ書き始める前なら1回だけやり直す
// (外した段は再計算される)。
func (e *Engine) RenderTo(ctx context.Context, p project.Project, prog Progress, sink Sink) (*Result, error) {
	ts := &startTrackSink{Sink: sink}
	res, err := e.renderTo(ctx, p, prog, ts)
	if err != nil && errors.Is(err, errSpoolLost) && !ts.started && ctx.Err() == nil {
		return e.renderTo(ctx, p, prog, ts)
	}
	return res, err
}

// startTrackSink は Sink が書き始めたかを覚える。
type startTrackSink struct {
	Sink
	started bool
}

func (s *startTrackSink) Start(frames, sampleRate int) error {
	s.started = true
	return s.Sink.Start(frames, sampleRate)
}

// renderTo はプロジェクト全体を、音源を流しながらレンダリングして sink へ書き出す。
// 曲の長さに依らず、メモリはほぼ一定(段の出力は一時ファイルに置く)。
//
// キャッシュ付きのエンジン(プレビュー)は、段ごとの出力(PA・直接音・残響・PA出力の帯域レベル)をキャッシュし、
// キーが一致する段は計算せずにスプールから読む。流れは、3つのパス:
//
//	パス0: PA入力のレベル合わせのために、音源ゲイン後の合計のラウドネスを測る(autoLevel が on で、結果が無いとき)
//	パス1: バス(音源 → PA、またはPAのスプール)→ 直接音・残響・帯域レベル → ミックスを一時ファイルへ
//	パス2: マスター(ラウドネス調整 → リミッタ)を掛けて sink へ
func (e *Engine) renderTo(ctx context.Context, p project.Project, prog Progress, sink Sink) (*Result, error) {
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
	chunk := e.chunkSize()

	// パス0: レベル合わせ(測定したときは、デコードの進捗はそちらで通知する)
	keys := sourceKeys(p.Sources)
	dp := newDecodeProgress(prog, len(p.Sources))
	alignDb, err := e.inputLevelStage(ctx, p, keys, dp)
	if err != nil {
		return nil, err
	}
	if dp.used() {
		dp = nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// 段のキャッシュを調べる。当たった段は、この実行の間だけ参照を持つ(終わったら返す)
	paKey := paKeyFor(p, keys, alignDb)
	dKey, rKey, sKey := directKeyFor(pp, paKey), reverbKeyFor(pp, paKey), paSpectrumKeyFor(paKey)
	var held []*spool
	defer func() {
		for _, s := range held {
			s.release()
		}
	}()
	lookup := func(slot, key string) *spool {
		if s, ok := e.cache.lookupSpool(slot, key); ok {
			held = append(held, s)
			return s
		}
		return nil
	}
	paSp, dSp, rSp := lookup("pa", paKey), lookup("direct", dKey), lookup("reverb", rKey)
	var spec paSpectrumOut
	hitSpec := false
	if e.analyzePA {
		if v, ok := e.cache.lookup("paSpectrum", sKey); ok {
			spec, hitSpec = v.(paSpectrumOut), true
		}
	}
	needD, needR, needSpec := dSp == nil, rSp == nil, e.analyzePA && !hitSpec
	needBus := needD || needR || needSpec

	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	// 残響は直接音より先に届かない: 最初に届く音(いちばん近いメインスピーカーの直接音)から残響が始まる。
	// プリディレイはそこからの遅れになる
	revDelay := firstArrivalSamples(p)

	// 曲の長さは、キャッシュに当たった段のメタから分かる。無ければ、バスを最後まで読んで分かる
	songLen, known := 0, false
	for _, s := range []*spool{paSp, dSp, rSp} {
		if s != nil {
			songLen, known = s.meta.SongLen, true
		}
	}
	outLen := songLen + revDelay + len(ir[0])

	// バスの入力
	var busR busReader
	var live *liveBus
	expected := songLen
	var paw *spoolWriter
	writers := make([]*spoolWriter, 4) // [0]=ミックス [1]=PA [2]=直接音 [3]=残響。失敗・中断・途中の失敗のときは、書きかけを消す
	defer func() {
		for _, w := range writers {
			if w != nil {
				w.Abort()
			}
		}
	}()
	var busMeter *dsp.LoudnessMeter
	if needBus {
		if paSp != nil {
			r, err := paSp.open(0)
			if err != nil {
				return nil, err
			}
			defer r.Close()
			busR = r
		} else {
			decs, err := e.openSources(ctx, p.Sources, dp)
			if err != nil {
				return nil, err
			}
			pas := make([]*paProc, len(p.Sources))
			for i, s := range p.Sources {
				pas[i] = newPAProc(sampleRate, s.GainDb+alignDb, p.PA)
			}
			live = newLiveBus(decs, pas, nil, chunk)
			defer live.close()
			busR, expected = live, expectedFrames(decs)
			if e.cache != nil { // バスはキャッシュにも残す
				if paw, err = newSpoolWriter(dir, 2); err != nil {
					return nil, err
				}
				writers[1] = paw
				busMeter = dsp.NewLoudnessMeter(sampleRate, 2)
			}
		}
	}

	// 直接音・残響の処理器と、当たった段のスプールの読み出し
	var dproc *directProc
	var rproc *reverbProc
	var dw, rw *spoolWriter
	var dsrc, rsrc mixSrc
	if needD {
		dproc = newDirectProc(pp)
		dsrc = dproc.out
		if e.cache != nil {
			if dw, err = newSpoolWriter(dir, 2); err != nil {
				return nil, err
			}
			writers[2] = dw
		}
	} else {
		m := &spoolMixSrc{s: dSp}
		defer m.close()
		dsrc = m
	}
	if needR {
		rproc = newReverbProc(pp, ir)
		rsrc = rproc.out
		if e.cache != nil {
			if rw, err = newSpoolWriter(dir, 2); err != nil {
				return nil, err
			}
			writers[3] = rw
		}
	} else {
		m := &spoolMixSrc{s: rSp}
		defer m.close()
		rsrc = m
	}
	var an *analysis.Analyzer
	var paMS analysis.MeanSquareAcc
	if needSpec {
		an = analysis.NewAnalyzer(sampleRate)
	}
	dtee, rtee := &queueTee{w: dw}, &queueTee{w: rw}

	mw, err := newSpoolWriter(dir, 2)
	if err != nil {
		return nil, err
	}
	writers[0] = mw
	abort := func(err error) (*Result, error) {
		for _, w := range writers {
			if w != nil {
				w.Abort()
			}
		}
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, err
	}

	mixMeter := dsp.NewLoudnessMeter(sampleRate, 2)
	prog.report(StageProcess, 0)
	mx := &mixer{g: mixGainsFor(p, pp.pr), revDelay: revDelay, d: dsrc, r: rsrc}
	mx.emit = func(buf [][]float32) error {
		mixMeter.Write(buf)
		if !needBus && outLen > 0 { // バスを読まないときの進捗は、ミックスの進み具合
			prog.report(StageProcess, float64(mx.pos+len(buf[0]))/float64(outLen))
		}
		return mw.Write(buf)
	}
	limit := math.MaxInt // 曲の長さが分からないうちは、入力がそろった所まで
	if known {
		limit = outLen
	}
	srcErr := func() error { return errors.Join(spoolErr(dsrc), spoolErr(rsrc)) }

	if needBus {
		buf := [][]float32{make([]float32, chunk), make([]float32, chunk)}
		frames := 0
		for {
			if err := ctx.Err(); err != nil {
				return abort(err)
			}
			n, err := busR.Read(buf)
			if err == io.EOF {
				break
			}
			if err != nil {
				return abort(err)
			}
			frames += n
			bus := [][]float32{buf[0][:n], buf[1][:n]}
			if paw != nil {
				busMeter.Write(bus)
				if err := paw.Write(bus); err != nil {
					return abort(err)
				}
			}
			var aerr error
			var wg sync.WaitGroup
			if needD {
				wg.Add(1)
				go func() { defer wg.Done(); dproc.Push(bus) }()
			}
			if needR {
				wg.Add(1)
				go func() { defer wg.Done(); rproc.Push(bus) }()
			}
			if needSpec {
				wg.Add(1)
				go func() {
					defer wg.Done()
					mono := analysis.Mono(bus[0], bus[1])
					paMS.Add(mono)
					aerr = an.Write(ctx, mono)
				}()
			}
			wg.Wait()
			if aerr == nil && needD {
				aerr = dtee.flush(dproc.out)
			}
			if aerr == nil && needR {
				aerr = rtee.flush(rproc.out)
			}
			if aerr == nil {
				aerr = mx.drain(limit)
			}
			if aerr == nil {
				aerr = srcErr()
			}
			if aerr != nil {
				return abort(aerr)
			}
			if expected > 0 {
				prog.report(StageProcess, float64(frames)/float64(expected))
			}
		}
		if known && frames != songLen {
			return abort(fmt.Errorf("render: キャッシュの長さ(%d)と音源の長さ(%d)が合いません", songLen, frames))
		}
		songLen = frames
		outLen = songLen + revDelay + len(ir[0])
		limit = outLen
	}

	// 入力の終わり: 尾を出し切って、ミックスを最後まで進める
	var wg sync.WaitGroup
	if needD {
		wg.Add(1)
		go func() { defer wg.Done(); dproc.Flush() }()
	}
	if needR {
		wg.Add(1)
		go func() { defer wg.Done(); rproc.Flush() }()
	}
	wg.Wait()
	var ferr error
	if needD {
		ferr = dtee.flush(dproc.out)
	}
	if ferr == nil && needR {
		ferr = rtee.flush(rproc.out)
	}
	if ferr == nil {
		ferr = mx.drain(limit)
	}
	if ferr == nil {
		ferr = srcErr()
	}
	if ferr == nil && mx.pos != outLen {
		ferr = fmt.Errorf("render: ミックスが %d フレームのはずが %d フレームでした", outLen, mx.pos)
	}
	if ferr != nil {
		return abort(ferr)
	}
	var series *analysis.Series
	if an != nil {
		if series, err = an.Finish(ctx); err != nil {
			return abort(err)
		}
	}

	// 計算した段をキャッシュに登録する(マスターの前に。中断されても、計算した段は次のプレビューで使える)
	mixSp, err := mw.Commit(spoolMeta{SongLen: songLen})
	if err != nil {
		return abort(err)
	}
	defer mixSp.release()
	writers[0] = nil
	commit := func(i int, slot, key string, meta spoolMeta) error {
		w := writers[i]
		if w == nil {
			return nil
		}
		writers[i] = nil
		s, err := w.Commit(meta)
		if err != nil {
			return err
		}
		e.cache.putSpool(slot, key, s)
		return nil
	}
	var cerr error
	if paw != nil {
		cerr = commit(1, "pa", paKey, spoolMeta{SongLen: songLen, BusLufs: busMeter.Integrated()})
	}
	if cerr == nil {
		cerr = commit(2, "direct", dKey, spoolMeta{SongLen: songLen})
	}
	if cerr == nil {
		cerr = commit(3, "reverb", rKey, spoolMeta{SongLen: songLen})
	}
	if cerr != nil {
		return abort(cerr)
	}
	if series != nil {
		spec = paSpectrumOut{series: series, meanSquare: paMS.Value()}
		e.cache.put("paSpectrum", sKey, spec)
	}
	prog.report(StageProcess, 1)

	// パス2: マスター(ラウドネス調整 → リミッタ)を掛けて sink へ
	outLufs, finalMS, err := masterPass(ctx, mixSp, outLen, songLen, mixMeter.Integrated(), p.Output, sink, prog)
	if err != nil {
		return nil, err
	}
	res := &Result{Frames: outLen, SampleRate: sampleRate, LUFS: outLufs}
	if e.analyzePA && spec.series != nil {
		res.PA = &PASpectrum{Series: spec.series, OffsetDb: levelOffsetFromMS(spec.meanSquare, finalMS)}
	}
	return res, nil
}

// spoolErr はミックスの入力がスプールの読み出しに失敗していれば、そのエラー。
func spoolErr(s mixSrc) error {
	if m, ok := s.(*spoolMixSrc); ok {
		return m.err
	}
	return nil
}

// runDir は、この実行のスプールを置くディレクトリと、終わったときの後始末を返す。
// キャッシュなしのエンジン(書き出し・設定でキャッシュを切ったプレビュー)は、実行ごとに作って消す(置き場所は e.baseDir)。
// キャッシュ付きは Engine の一時ディレクトリを使う。
func (e *Engine) runDir() (string, func(), error) {
	if e.cache != nil {
		d, err := e.tempDir()
		return d, func() {}, err
	}
	d, err := mkTempDir(e.baseDir, renderTempPrefix)
	if err != nil {
		return "", nil, err
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
