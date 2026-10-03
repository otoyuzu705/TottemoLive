package render

import (
	"context"
	"errors"
	"math"

	"tottemolive/internal/crowd"
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
	"tottemolive/internal/venue"
)

// WindowSeconds は先行プレビューの長さ(秒)。曲全体の処理が終わるまでの間、ここまでを再生できる。
const WindowSeconds = 30

// minBusSec は、先行プレビューの窓に最低限含める、PA出力(曲の音)の長さ(秒)。
const minBusSec = 3

// Window は先行プレビュー(曲の一部だけを先に処理した結果)。
type Window struct {
	Audio      [][]float32
	SampleRate int
	// StartSec は、Audio の先頭が曲頭から何秒の位置か。
	StartSec float64
	// TotalSec は、曲全体(残響の尾を含む)の長さ(秒)。
	TotalSec float64
}

// PreviewWindow は、startSec(曲頭からの秒)から WindowSeconds ぶんだけを、曲全体と同じ処理でレンダリングする。
// シークした位置の周辺を先に聴けるようにするためのもので、曲全体の処理(Preview)が終わるまでの間に使う。
//
// 処理は曲全体と同じで、違うのは次の点だけ:
//   - PA段(デコード・レベル合わせ・EQ・コンプ・歪み)は曲全体を処理する(コンプは曲の頭から状態を持つため。
//     結果は段のキャッシュに入るので、続く曲全体の処理はこの段を再計算しない)
//   - 直接音・残響・客席は、窓の範囲だけを計算する。残響・直接音の遅れで窓より前の音が窓に届くので、
//     バスは窓より IR の長さ + 最大の伝搬遅延だけ前から使う(窓の範囲の結果は、曲全体の同じ範囲と一致する)
//   - ラウドネス調整のゲインは、曲全体の出力がまだ無いので推定する(estimateMasterGainDb)
//
// 直接音・残響・客席の結果は段のキャッシュには入れない(キャッシュは曲全体の出力を持つ段のためのもの)。
func (e *Engine) PreviewWindow(ctx context.Context, p project.Project, startSec float64) (*Window, error) {
	return e.previewWindow(ctx, p, startSec, WindowSeconds)
}

// previewWindow は PreviewWindow の、窓の長さ(秒)を指定できる版(テスト用に短くする)。
func (e *Engine) previewWindow(ctx context.Context, p project.Project, startSec, widthSec float64) (*Window, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	p = pp.p
	srcs := e.sources(p.Sources, nil)
	level, err := e.inputLevelStage(ctx, p, srcs)
	if err != nil {
		return nil, err
	}
	pa, err := e.paStage(ctx, p, srcs, level, newSteps(nil, StageProcess, len(srcs)))
	if err != nil {
		return nil, err
	}
	songLen := busLen(pa.bufs)
	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	revDelay := firstArrivalSamples(p)
	outLen := songLen + revDelay + len(ir[0])

	// 窓 [s0, s1)(出力の時刻)。曲の終わり近くでも、長さを保つように手前へずらす
	width := int(widthSec * sampleRate)
	// ラウドネスの推定にはバスの音が要るので、曲の終わりより minBusSec 以上手前から始める
	// (残響の尾だけの窓では、バスとの比較ができない)
	last := min(max(outLen-width, 0), max(songLen+revDelay-minBusSec*sampleRate, 0))
	s0 := min(max(int(math.Round(startSec*sampleRate)), 0), last)
	s1 := min(s0+width, outLen)
	// 窓に届く音は、窓より前のバスにもある(残響の尾と伝搬遅延)。その長さぶん手前から計算する
	maxDelay := int(math.Ceil(venue.MaxDistanceM(pp.pr) / spatial.SpeedOfSound * sampleRate))
	a := max(s0-len(ir[0])-maxDelay, 0)
	seg := sumBusRange(pa.bufs, a, min(s1, songLen))
	n := s1 - a // 区間の出力の長さ(区間の先頭 = 曲の a サンプル目)

	var direct, reverb, crowdSig [][]float32
	var derr, rerr, cerr error
	done := make(chan struct{}, 3)
	go func() { direct, derr = directCompute(ctx, pp, seg, n); done <- struct{}{} }()
	go func() { reverb, rerr = reverbCompute(ctx, pp, seg, ir, n); done <- struct{}{} }()
	go func() {
		crowdSig, cerr = crowd.Render(ctx, p.Crowd, p.Listener, pp.set, sampleRate, s0, s1-s0)
		done <- struct{}{}
	}()
	for range 3 {
		<-done
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(derr, rerr, cerr); err != nil {
		return nil, err
	}

	// 出力の時刻 [s0, s1) にそろえる。残響は、最初の直達音(revDelay)から始まる
	directW := cropRange(direct, s0-a, s1-a)
	reverbW := cropRange(reverb, s0-a-revDelay, s1-a-revDelay)
	gains := mixGainsFor(p, pp.pr)
	out := mix(gains, directW, reverbW, crowdSig, 0)

	if s0 == 0 && s1 == outLen {
		master(out, sampleRate, p.Output) // 窓が曲全体なら、推定せず正確に測れる
	} else {
		music := mix(mixGains{direct: gains.direct, reverb: gains.reverb}, directW, reverbW, crowdSig, 0) // 客席を除いた音楽
		gainDb, ok, err := e.estimateMasterGainDb(ctx, pp, pa, music, out, gains, s0, s1, revDelay, songLen, outLen)
		if err != nil {
			return nil, err
		}
		if ok {
			g := float32(dsp.DbToLin(gainDb))
			for _, ch := range out {
				for i := range ch {
					ch[i] *= g
				}
			}
		}
		dsp.TruePeakLimit(out, sampleRate, p.Output.CeilingDbTp)
	}
	return &Window{Audio: out, SampleRate: sampleRate, StartSec: float64(s0) / sampleRate, TotalSec: float64(outLen) / sampleRate}, nil
}

// estimateMasterGainDb は、先行プレビューに掛けるラウドネス調整のゲイン(dB)を推定する。
// 曲全体の出力のラウドネスは、曲全体を処理し終わるまで分からない。そこで、次のように推定する。
//
//   - 音楽(直接音 + 残響): ミックス後の音楽とPA出力(バス)のラウドネスの差は、直接音・残響が時間に依らない
//     線形の処理なので、曲のどの位置でもほぼ一定になる。窓での差(音楽 − バス)を、曲全体のバスのラウドネスに足して、
//     曲全体の音楽のラウドネスにする(窓だけのラウドネスで合わせると、サビと静かな部分とで音量が大きく変わる)
//   - 客席: 曲全体を処理すると時間がかかる(曲全体の音楽の処理と同じくらい)ので、曲に散らした数か所を
//     処理して、そのラウドネスで代用する
//   - 2つを、パワーの和で足す(客席は音楽と無相関なので)
//
// 窓が無音などで測れないときは ok=false(ゲインを掛けない)。music は客席を除いた窓の出力、out は客席を含む窓の出力
// (どちらもゲインを掛ける前。曲の s0〜s1)。
func (e *Engine) estimateMasterGainDb(ctx context.Context, pp *prepared, pa paResult, music, out [][]float32, g mixGains,
	s0, s1, revDelay, songLen, outLen int) (gainDb float64, ok bool, err error) {
	whole, err := memo(e.cache, "busLufs", hashKey(pa.key), func() (float64, error) {
		return dsp.IntegratedLUFS(sumBusRange(pa.bufs, 0, songLen), sampleRate), nil
	})
	if err != nil {
		return 0, false, err
	}
	// 窓の出力は、バスより revDelay ぶん遅れて届くので、同じ音の区間で比べる
	b0, b1 := min(max(s0-revDelay, 0), songLen), min(max(s1-revDelay, 0), songLen)
	busWin := dsp.IntegratedLUFS(sumBusRange(pa.bufs, b0, b1), sampleRate)
	musicWin := dsp.IntegratedLUFS(music, sampleRate)
	if bad(whole) || bad(busWin) || bad(musicWin) {
		return 0, false, nil
	}
	total := math.Pow(10, (whole+(musicWin-busWin))/10)
	if g.crowd > 0 {
		c, err := crowdSampleLufs(ctx, pp, g.crowd, outLen)
		if err != nil {
			return 0, false, err
		}
		if !bad(c) {
			total += math.Pow(10, c/10)
		}
	}
	return pp.p.Output.TargetLufs - 10*math.Log10(total), true, nil
}

// 客席のラウドネスの見積もりに使う区間の数と長さ(秒)
const (
	crowdSamples   = 6
	crowdSampleSec = 3
)

// crowdSampleLufs は、曲全体(outLen)に等間隔に散らした区間の客席信号(ゲイン gain を掛けたもの)のラウドネスを返す。
// 無音(客席なし)なら -Inf。
func crowdSampleLufs(ctx context.Context, pp *prepared, gain float32, outLen int) (float64, error) {
	p := pp.p
	n := crowdSampleSec * sampleRate
	if outLen <= crowdSamples*n {
		sig, err := crowd.Render(ctx, p.Crowd, p.Listener, pp.set, sampleRate, 0, outLen)
		return crowdLufs(sig, gain), err
	}
	joined := [][]float32{nil, nil}
	for k := 0; k < crowdSamples; k++ {
		start := (outLen - n) * k / (crowdSamples - 1)
		sig, err := crowd.Render(ctx, p.Crowd, p.Listener, pp.set, sampleRate, start, n)
		if err != nil {
			return 0, err
		}
		for c := range joined {
			joined[c] = append(joined[c], sig[c]...)
		}
	}
	return crowdLufs(joined, gain), nil
}

func crowdLufs(sig [][]float32, gain float32) float64 {
	scaled := make([][]float32, len(sig))
	for c, ch := range sig {
		scaled[c] = make([]float32, len(ch))
		for i, v := range ch {
			scaled[c][i] = v * gain
		}
	}
	return dsp.IntegratedLUFS(scaled, sampleRate)
}

func bad(v float64) bool { return math.IsInf(v, 0) || math.IsNaN(v) }

// sumBusRange は、音源ごとのPA出力の [from, to) を足し合わせる(区間の外・音源の外は無音)。
func sumBusRange(bufs [][][]float32, from, to int) [][]float32 {
	n := max(to-from, 0)
	bus := [][]float32{make([]float32, n), make([]float32, n)}
	for _, s := range bufs {
		for c := range bus {
			if from >= len(s[c]) {
				continue
			}
			for i, v := range s[c][from:min(to, len(s[c]))] {
				bus[c][i] += v
			}
		}
	}
	return bus
}

// cropRange は buf の [from, to) を返す。範囲の外(負の添字や末尾の先)は無音で埋める。
func cropRange(buf [][]float32, from, to int) [][]float32 {
	out := make([][]float32, len(buf))
	for c, ch := range buf {
		out[c] = make([]float32, max(to-from, 0))
		for i := range out[c] {
			if j := from + i; j >= 0 && j < len(ch) {
				out[c][i] = ch[j]
			}
		}
	}
	return out
}
