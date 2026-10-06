package render

import "math"

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
