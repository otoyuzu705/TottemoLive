package render

import (
	"context"
	"math"
	"sync"

	"tottemolive/internal/dsp"
	"tottemolive/internal/spatial"
)

// streamBlock は、曲全体を処理器に流すときの1回のフレーム数(包みの関数・窓が使う)。
const streamBlock = 65536

// directProc は左右のPAスピーカーを仮想スピーカーとして置き、リスナーの耳に届く直接音を作る処理器。
// スピーカーが1本ならモノラル和を、2本以上ならチャンネルを順に割り当てる(L,R,L,R...)。
// サブウーファーが有効なときは、PA出力をクロスオーバーで分け、メインには中高域だけを送り、
// 低域はサブ経路(左右のモノ和 → 距離減衰・遅延 → 両耳に同じ信号)で足す。
// バスを区切って順に Push してよい(結果は区切り方に依らない)。
//
// 足し算の順序は、スピーカーの順、続いてサブの順で固定(全体を一度に処理した場合とビット単位で一致する)。
type directProc struct {
	pp     *prepared
	subOn  bool
	spk    []*spatial.DirectStream
	hp     []*dsp.LR4 // スピーカーごとの高域通過(サブ有効時)
	subLP  *dsp.LR4
	subG   float32
	subs   []*spatial.SubStream
	q      []*frameQueue // スピーカー(先頭)、サブ(続き)ごとの出力
	out    *frameQueue   // 合算した直接音
	feeds  [][]float32
	mono   []float32
	flushd bool
}

func newDirectProc(pp *prepared) *directProc {
	p := pp.p
	d := &directProc{pp: pp, subOn: subsActive(p), out: newFrameQueue(2)}
	for _, s := range p.Venue.Speakers {
		az, el, dist := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
		d.spk = append(d.spk, spatial.NewDirectStream(sampleRate, dist, az, el, pp.set, spatial.DirectParams{
			Rolloff: p.Spatial.DistanceRolloff, AirAbsorption: p.Spatial.AirAbsorption,
			AirComp: airCompFor(p, pp.pr, s),
		}))
		d.q = append(d.q, newFrameQueue(2))
		if d.subOn {
			d.hp = append(d.hp, dsp.NewLR4HighPass(sampleRate, p.Sub.CrossoverHz))
		}
	}
	d.feeds = make([][]float32, len(d.spk))
	if d.subOn {
		d.subLP = dsp.NewLR4LowPass(sampleRate, p.Sub.CrossoverHz)
		d.subG = float32(dsp.DbToLin(p.Sub.LevelDb) / float64(len(p.Venue.Subs)))
		extra := subAlignDelays(p, pp.pr)
		for i, s := range p.Venue.Subs {
			_, _, dist := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
			d.subs = append(d.subs, spatial.NewSubStream(sampleRate, dist, extra[i], p.Spatial.DistanceRolloff))
			d.q = append(d.q, newFrameQueue(1))
		}
	}
	return d
}

// Push はバスの続き(ステレオ)を与える。バスは Push の間だけ読む(呼び出し側が書き換えてはいけない)。
func (d *directProc) Push(bus [][]float32) {
	n := len(bus[0])
	// スピーカーごとに並列に処理する(各スピーカーは自分の状態と待ち行列だけを触る)
	var wg sync.WaitGroup
	for i := range d.spk {
		wg.Add(1)
		go func() {
			defer wg.Done()
			feed := speakerFeed(bus, i, len(d.spk))
			if d.subOn {
				// 低域はサブが受け持つので、メインは中高域だけにする(元のバスは他の経路が使うので複製して掛ける)
				feed = append(d.feeds[i][:0], feed...)
				d.feeds[i] = feed
				d.hp[i].Process(feed)
			}
			l, r := d.spk[i].Process(feed)
			d.q[i].push([][]float32{l, r})
		}()
	}
	if d.subOn {
		// バスの左右の合計(モノ)の低域を、サブの台数で割って(合計が levelDb になるように)サブごとに処理する
		if cap(d.mono) < n {
			d.mono = make([]float32, n)
		}
		mono := d.mono[:n]
		for i := range mono {
			mono[i] = bus[0][i] + bus[1][i]
		}
		d.subLP.Process(mono)
		for i := range mono {
			mono[i] *= d.subG
		}
		for j, s := range d.subs {
			d.q[len(d.spk)+j].push([][]float32{s.Process(mono)})
		}
	}
	wg.Wait()
	d.drain()
}

// Flush は入力の終わりを知らせ、残り(HRIR・空気吸収の尾)を出す。
func (d *directProc) Flush() {
	if d.flushd {
		return
	}
	d.flushd = true
	for i, s := range d.spk {
		l, r := s.Flush()
		d.q[i].push([][]float32{l, r})
		d.q[i].end()
	}
	for j := range d.subs {
		d.q[len(d.spk)+j].end() // サブは尾がない
	}
	d.drain()
}

// drain は、全ストリームの出力がそろった所まで合算して out に出す。
// 合算は 0 + スピーカー0 + スピーカー1 + ... + サブ0 + サブ1 の順(終わった(ended)ストリームの先は無音なので足さない)。
func (d *directProc) drain() {
	limit, anyOpen, maxEnd := math.MaxInt, false, 0
	for _, q := range d.q {
		if !q.ended {
			anyOpen = true
			limit = min(limit, q.produced)
		}
		maxEnd = max(maxEnd, q.produced)
	}
	if !anyOpen {
		limit = maxEnd
	}
	from := d.out.produced
	if n := limit - from; n > 0 {
		sum := [][]float32{make([]float32, n), make([]float32, n)}
		for k, q := range d.q {
			for c := range sum {
				ch := c
				if k >= len(d.spk) {
					ch = 0 // サブはモノ(両耳に同じ信号)
				}
				for i := 0; i < n; i++ {
					if j := from + i; j >= q.start && j < q.produced {
						sum[c][i] += q.ch[ch][j-q.start]
					}
				}
			}
		}
		d.out.push(sum)
		for _, q := range d.q {
			q.discardBefore(limit)
		}
	}
	if !anyOpen {
		d.out.end()
	}
}

// speakerFeed はスピーカー i への入力(スピーカーが1本ならバスの左右平均、2本以上ならチャンネルを順に割り当てる)。
// 2本以上のときはバスのスライスをそのまま返すので、返したものは Push の間だけ読むこと
// (バスの置き場は、次のチャンクの先読みで上書きされうる)。
func speakerFeed(bus [][]float32, i, count int) []float32 {
	if count == 1 {
		m := make([]float32, len(bus[0]))
		for k := range m {
			m[k] = (bus[0][k] + bus[1][k]) / 2
		}
		return m
	}
	return bus[i%2]
}

// directCompute は、バス in(曲の先頭からの信号)から直接音を計算する。長さは total。
// 先行プレビューは、曲の一部を切り出したバスに対して同じ計算を呼ぶ。処理器 directProc に流す包み。
func directCompute(ctx context.Context, pp *prepared, in [][]float32, total int) ([][]float32, error) {
	d := newDirectProc(pp)
	for from := 0; from < len(in[0]); from += streamBlock {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		end := min(from+streamBlock, len(in[0]))
		d.Push([][]float32{in[0][from:end], in[1][from:end]})
	}
	d.Flush()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.out.collect(total), nil
}
