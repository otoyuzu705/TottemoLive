package render

import (
	"context"
	"fmt"
	"io"
	"math"

	"tottemolive/internal/analysis"
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

// mixBlock は、ミックスが1回に処理するフレーム数。
const mixBlock = 65536

// mixer は直接音・残響を、それぞれのゲインで足す。
// out[j] = direct[j]·gd + reverb[j-revDelay]·gr(残響は、リスナーに最初の音が届く時刻 revDelay から始まる)。
// 加算の順序は、直接音 × gd が先で、残響 × gr を後から足す。
type mixer struct {
	g        mixGains
	revDelay int
	pos      int // 次に出力するフレームの位置
	d, r     mixSrc
	emit     func(buf [][]float32) error
	buf      [][]float32
	rbuf     [][]float32
}

// drain は、入力がそろっている所まで(limit は超えない)、ミックスして emit に渡す。
// 終わりが確定した入力の先は無音として扱う(出力の長さは limit が決める)。
func (m *mixer) drain(limit int) error {
	if m.buf == nil {
		m.buf = [][]float32{make([]float32, mixBlock), make([]float32, mixBlock)}
		m.rbuf = [][]float32{make([]float32, mixBlock), make([]float32, mixBlock)}
	}
	for m.pos < limit {
		n := min(limit-m.pos, mixBlock, m.d.availFrom(m.pos))
		rpos := m.pos - m.revDelay
		if ra := m.r.availFrom(max(rpos, 0)); ra != math.MaxInt { // 残響は rpos から n 個ぶん必要
			n = min(n, ra+max(-rpos, 0))
		}
		if n <= 0 {
			return nil
		}
		out := [][]float32{m.buf[0][:n], m.buf[1][:n]}
		m.d.readAt(m.pos, out)
		for c := range out {
			for i := range out[c] {
				out[c][i] *= m.g.direct
			}
		}
		rb := [][]float32{m.rbuf[0][:n], m.rbuf[1][:n]}
		m.r.readAt(rpos, rb)
		for c := range out {
			for i := max(-rpos, 0); i < n; i++ {
				out[c][i] += rb[c][i] * m.g.reverb
			}
		}
		if err := m.emit(out); err != nil {
			return err
		}
		m.pos += n
		if q, ok := m.d.(interface{ discardBefore(int) }); ok {
			q.discardBefore(m.pos)
		}
		if q, ok := m.r.(interface{ discardBefore(int) }); ok {
			q.discardBefore(m.pos - m.revDelay)
		}
	}
	return nil
}

// mix は直接音・残響を、それぞれのゲインで足す。長さは直接音と同じ(残響の、それより後ろは捨てる)。mixer に全体を渡す包み。
func mix(g mixGains, direct, reverb [][]float32, reverbDelay int) [][]float32 {
	dq, rq := newFrameQueue(2), newFrameQueue(2)
	dq.push(direct)
	dq.end()
	rq.push(reverb)
	rq.end()
	n := len(direct[0])
	out := [][]float32{make([]float32, 0, n), make([]float32, 0, n)}
	m := &mixer{g: g, revDelay: reverbDelay, d: dq, r: rq, emit: func(buf [][]float32) error {
		for c := range out {
			out[c] = append(out[c], buf[c]...)
		}
		return nil
	}}
	_ = m.drain(n)
	return out
}

// masterPass はマスター(ラウドネス調整 → トゥルーピークリミッタ)を、ミックス済みのスプール in に掛けて sink へ流す。
// lufs はミックス全体の統合ラウドネス(有限のときだけ、目標に合わせるゲインを掛ける)。
// 戻り値は、出力全体の統合ラウドネスと、出力の曲の長さ(songLen フレーム)までの左右平均の2乗平均。
func masterPass(ctx context.Context, in *spool, outLen, songLen int, lufs float64, o project.Output, sink Sink, prog Progress) (outLufs, finalMS float64, err error) {
	if err := sink.Start(outLen, sampleRate); err != nil {
		return 0, 0, err
	}
	r, err := in.open(0)
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()
	apply := !math.IsInf(lufs, 0) && !math.IsNaN(lufs)
	var g float32
	if apply {
		g = float32(dsp.DbToLin(o.TargetLufs - lufs))
	}
	lim := dsp.NewLimiter(sampleRate, 2, o.CeilingDbTp)
	meter := dsp.NewLoudnessMeter(sampleRate, 2)
	var ms analysis.MeanSquareAcc
	pos := 0
	emit := func(out [][]float32) error {
		if len(out[0]) == 0 {
			return nil
		}
		if err := sink.Write(out); err != nil {
			return err
		}
		meter.Write(out)
		if pos < songLen {
			k := min(len(out[0]), songLen-pos)
			ms.Add(analysis.Mono(out[0][:k], out[1][:k]))
		}
		pos += len(out[0])
		prog.report(StageEncode, float64(pos)/float64(outLen))
		return nil
	}
	prog.report(StageEncode, 0)
	buf := [][]float32{make([]float32, mixBlock), make([]float32, mixBlock)}
	for {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		n, rerr := r.Read(buf)
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, 0, rerr
		}
		chunk := [][]float32{buf[0][:n], buf[1][:n]}
		if apply {
			for _, ch := range chunk {
				for i := range ch {
					ch[i] *= g
				}
			}
		}
		if err := emit(lim.Process(chunk)); err != nil {
			return 0, 0, err
		}
	}
	if err := emit(lim.Flush()); err != nil {
		return 0, 0, err
	}
	if pos != outLen {
		return 0, 0, fmt.Errorf("render: 出力が %d フレームのはずが %d フレームでした", outLen, pos)
	}
	return meter.Integrated(), ms.Value(), nil
}
