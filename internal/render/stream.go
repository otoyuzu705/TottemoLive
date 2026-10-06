package render

import "math"

// frameQueue は、流し処理の段の出力を、次の段が読み出すまでためておく待ち行列。
// 位置は曲頭(その段の出力の先頭)からの絶対位置。終わり(ended)が確定すると、その先は無音として読める。
type frameQueue struct {
	ch       [][]float32 // チャンネル別。ch[c][0] の絶対位置が start
	start    int
	produced int // これまでに push したフレーム数(絶対位置の終わり)
	ended    bool
}

func newFrameQueue(channels int) *frameQueue {
	return &frameQueue{ch: make([][]float32, channels)}
}

// push は buf(チャンネル別)を続きとして加える(複製して持つ)。
func (q *frameQueue) push(buf [][]float32) {
	for c := range q.ch {
		q.ch[c] = append(q.ch[c], buf[c]...)
	}
	q.produced += len(buf[0])
}

// end は、これ以上 push しないことを知らせる。
func (q *frameQueue) end() { q.ended = true }

// availFrom は位置 pos から読めるフレーム数。終わりが確定していれば、その先は無音なので上限はない。
func (q *frameQueue) availFrom(pos int) int {
	if q.ended {
		return math.MaxInt
	}
	return max(q.produced-pos, 0)
}

// readAt は位置 pos から dst の長さぶんを読む。まだ push されていない所(終わりの先)は無音。
func (q *frameQueue) readAt(pos int, dst [][]float32) {
	for c := range dst {
		for i := range dst[c] {
			if j := pos + i; j >= q.start && j < q.produced {
				dst[c][i] = q.ch[c][j-q.start]
			} else {
				dst[c][i] = 0
			}
		}
	}
}

// discardBefore は位置 pos より前を捨てる(もう読まない)。
func (q *frameQueue) discardBefore(pos int) {
	d := min(pos, q.produced) - q.start
	if d <= 0 {
		return
	}
	for c := range q.ch {
		q.ch[c] = q.ch[c][:copy(q.ch[c], q.ch[c][d:])]
	}
	q.start += d
}

// mixSrc は、ミックスが読む入力(直接音・残響)。
type mixSrc interface {
	availFrom(pos int) int
	readAt(pos int, dst [][]float32)
}

// collect は、待ち行列の [0, n) をまとめて返す(終わりの先は無音)。窓・テスト用。
func (q *frameQueue) collect(n int) [][]float32 {
	out := make([][]float32, len(q.ch))
	for c := range out {
		out[c] = make([]float32, n)
	}
	q.readAt(0, out)
	return out
}
