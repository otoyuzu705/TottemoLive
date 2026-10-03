package render

import (
	"context"
	"errors"
	"math"

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

	var direct, reverb [][]float32
	var derr, rerr error
	done := make(chan struct{}, 2)
	go func() { direct, derr = directCompute(ctx, pp, seg, n); done <- struct{}{} }()
	go func() { reverb, rerr = reverbCompute(ctx, pp, seg, ir, n); done <- struct{}{} }()
	for range 2 {
		<-done
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(derr, rerr); err != nil {
		return nil, err
	}

	// 出力の時刻 [s0, s1) にそろえる。残響は、最初の直達音(revDelay)から始まる
	directW := cropRange(direct, s0-a, s1-a)
	reverbW := cropRange(reverb, s0-a-revDelay, s1-a-revDelay)
	gains := mixGainsFor(p, pp.pr)
	out := mix(gains, directW, reverbW, 0)

	if s0 == 0 && s1 == outLen {
		master(out, sampleRate, p.Output) // 窓が曲全体なら、推定せず正確に測れる
	} else {
		gainDb, ok, err := e.estimateMasterGainDb(p, pa, out, s0, s1, revDelay, songLen)
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
// 曲全体の出力のラウドネスは、曲全体を処理し終わるまで分からない。一方、直接音・残響・ミックスは時間に依らない
// 線形の処理なので、「ミックス後の出力のラウドネス」と「PA出力(バス)のラウドネス」の差は、曲のどの位置でも
// ほぼ一定になる。そこで、窓での差(出力 − バス)を、曲全体のバスのラウドネスに足して、曲全体の出力の
// ラウドネスを推定する(窓だけのラウドネスで合わせると、サビと静かな部分とで音量が大きく変わってしまう)。
// 窓が無音などで測れないときは ok=false(ゲインを掛けない)。
// out はゲインを掛ける前の窓の出力(曲の s0〜s1)。
func (e *Engine) estimateMasterGainDb(p project.Project, pa paResult, out [][]float32,
	s0, s1, revDelay, songLen int) (gainDb float64, ok bool, err error) {
	whole, err := memo(e.cache, "busLufs", hashKey(pa.key), func() (float64, error) {
		return dsp.IntegratedLUFS(sumBusRange(pa.bufs, 0, songLen), sampleRate), nil
	})
	if err != nil {
		return 0, false, err
	}
	// 窓の出力は、バスより revDelay ぶん遅れて届くので、同じ音の区間で比べる
	b0, b1 := min(max(s0-revDelay, 0), songLen), min(max(s1-revDelay, 0), songLen)
	busWin := dsp.IntegratedLUFS(sumBusRange(pa.bufs, b0, b1), sampleRate)
	outWin := dsp.IntegratedLUFS(out, sampleRate)
	if bad(whole) || bad(busWin) || bad(outWin) {
		return 0, false, nil
	}
	return p.Output.TargetLufs - (whole + (outWin - busWin)), true, nil
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
