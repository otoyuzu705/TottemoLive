// Package spatial はHRIRの読み込み、距離モデル、仮想スピーカーからリスナーへの直接音の計算を持つ。
// 座標はステージ中央を原点としたメートル単位(x 右、y 客席側、z 高さ)。
package spatial

import (
	"context"
	"fmt"
	"math"

	"tottemolive/internal/dsp"
)

// 距離モデルの形を決める定数。強さそのものは Project の spatial.* で調整する。
const (
	SpeedOfSound = 343.0 // m/s
	// RefDistanceM は距離減衰の基準距離(ゲイン1)。これより近い音は大きくなり、最大 MaxGain まで持ち上げる。
	RefDistanceM = 10.0
	MaxGain      = 4.0
)

// HRIR は片方向のインパルス応答(左右の耳)。
type HRIR struct {
	L, R []float32
}

// Set はHRIRの集合。方位角は正面0°・右が正、仰角は水平0°・上が正。
type Set interface {
	Name() string
	Lookup(azDeg, elDeg float64) HRIR
}

const (
	SyntheticName = "synthetic"
	BrownDudaName = "brown-duda"
)

// SetNames は読み込める同梱HRIRセットの名前。params の spatial.hrirSet の選択肢と同じ順(テストで確認)。
func SetNames() []string { return []string{SyntheticName, BrownDudaName} }

// SetOptions はHRIRセットの調整(Project の spatial.* から作る)。
type SetOptions struct {
	// HeadShadow は頭の影(反対側の耳の高域の遮りと、同じ側の耳の持ち上がり)の強さ。dBで表した影の量の倍率で、
	// 1 でモデルの値、0 で影なし(両耳間時間差だけ)。spatial.headShadow。
	HeadShadow float64
}

// LoadSet はHRIRセットを返す。同梱の実測HRIR(WAV + JSON)は素材ごとの再配布条件を確認してから追加する。
// Set は方向ごとのHRIRを作って保持するので、o が違えば別の Set を作る(レンダリングごとに作る)。
func LoadSet(name string, sr int, o SetOptions) (Set, error) {
	switch name {
	case SyntheticName:
		return newSynthetic(sr, o.HeadShadow), nil
	case BrownDudaName:
		return newBrownDuda(sr, o.HeadShadow), nil
	}
	return nil, fmt.Errorf("spatial: unknown HRIR set %q", name)
}

// Gain は距離 d(m) での減衰ゲイン。rolloff=1 で逆距離則。基準距離 RefDistanceM でゲイン1で、
// それより近いと大きくなり(最大 MaxGain = +12 dB)、遠いと小さくなる。
func Gain(d, rolloff float64) float64 {
	return math.Min(math.Pow(RefDistanceM/math.Max(d, 1e-3), rolloff), MaxGain)
}

// DelaySamples は距離 d(m) の伝搬遅延(サンプル数、四捨五入)。
func DelaySamples(d float64, sr int) int {
	return int(math.Round(d / SpeedOfSound * float64(sr)))
}

// Direction はリスナーから見た点の方位角・仰角(度)と距離(m)を返す。
// リスナーの向き yawDeg=0 は -y(ステージ方向)、正で右回り。
func Direction(lx, ly, lz, yawDeg, px, py, pz float64) (azDeg, elDeg, dist float64) {
	dx, dy, dz := px-lx, py-ly, pz-lz
	yaw := yawDeg * math.Pi / 180
	front := dx*math.Sin(yaw) - dy*math.Cos(yaw)
	right := dx*math.Cos(yaw) + dy*math.Sin(yaw)
	horiz := math.Hypot(dx, dy)
	azDeg = math.Atan2(right, front) * 180 / math.Pi
	elDeg = math.Atan2(dz, horiz) * 180 / math.Pi
	dist = math.Sqrt(dx*dx + dy*dy + dz*dz)
	return
}

// DirectParams は仮想スピーカー1本ぶんの直接音の計算に使うパラメーター。
type DirectParams struct {
	Rolloff       float64 // spatial.distanceRolloff
	AirAbsorption float64 // spatial.airAbsorption
}

// DirectStream は仮想スピーカー1本ぶんの直接音を、入力を区切って順に Process しながら計算する。
// 距離減衰・空気吸収・伝搬遅延・HRIR畳み込みを通した左右の耳の信号になる(Direct と同じ計算)。
// 結果は、全体を一度に計算した場合と、区切り方に依らずビット単位で一致する。
type DirectStream struct {
	pad     int // 先頭に入れるゼロの数(伝搬遅延 - 空気吸収の群遅延)
	g       float32
	started bool
	conv    *dsp.StreamConvolver
	x       []float32
	l, r    []float32
}

// NewDirectStream は、距離 dist(m)・方位 azDeg・仰角 elDeg のスピーカーの直接音を計算する処理器を返す。
func NewDirectStream(sr int, dist, azDeg, elDeg float64, set Set, p DirectParams) *DirectStream {
	// 空気吸収(線形位相FIR)の群遅延ぶん、伝搬遅延から引いて、全体の遅れを合わせる(近すぎて引けないぶんは遅れる)
	air := AirFIR(dist, p.AirAbsorption, sr)
	delay := DelaySamples(dist, sr)
	lead := 0
	if air != nil {
		lead = min(delay, AirGroupDelay)
	}
	h := set.Lookup(azDeg, elDeg)
	hl, hr := h.L, h.R
	if air != nil {
		// 吸収は、距離ごとの小さなFIRなので、HRIRと先に畳み込んで1回の畳み込みにする(長さは512以下に収まる)
		hl, hr = convolveFIR(h.L, air), convolveFIR(h.R, air)
	}
	return &DirectStream{pad: delay - lead, g: float32(Gain(dist, p.Rolloff)), conv: dsp.NewStreamConvolver(hl, hr)}
}

// Process は入力 in の続きを与え、新しく確定した左右の耳の信号を返す。戻り値は次の Process / Flush まで有効。
// 最初の呼び出しでは、伝搬遅延ぶんのゼロを先に流す。
func (d *DirectStream) Process(in []float32) (l, r []float32) {
	n := len(in)
	if !d.started {
		n += d.pad
	}
	if cap(d.x) < n {
		d.x = make([]float32, n)
	}
	x := d.x[:n]
	off := 0
	if !d.started {
		clear(x[:d.pad])
		off = d.pad
		d.started = true
	}
	for i, v := range in {
		x[off+i] = v * d.g
	}
	out := d.conv.Process(x)
	return out[0], out[1]
}

// Flush は入力の終わりを知らせ、残りの出力(HRIR・空気吸収の尾)を返す。
// 左右の長さは同じにそろえる(短いほうは無音で埋める)。
func (d *DirectStream) Flush() (l, r []float32) {
	if !d.started && d.pad > 0 { // 入力が1つもなかった: 遅延ぶんのゼロだけを流す
		pl, pr := d.Process(nil)
		l, r = append(l, pl...), append(r, pr...)
	}
	out := d.conv.Flush()
	l, r = append(l, out[0]...), append(r, out[1]...)
	for len(l) < len(r) {
		l = append(l, 0)
	}
	for len(r) < len(l) {
		r = append(r, 0)
	}
	return l, r
}

// Direct はスピーカーへの入力 in を、距離減衰・空気吸収・伝搬遅延・HRIR畳み込みを通した
// 左右の耳の信号にして返す。出力長は len(in)+遅延+len(HRIR)-1。DirectStream に全体を流す包み。
func Direct(ctx context.Context, in []float32, sr int, dist, azDeg, elDeg float64, set Set, p DirectParams) ([][]float32, error) {
	d := NewDirectStream(sr, dist, azDeg, elDeg, set, p)
	var l, r []float32
	const chunk = 65536
	for from := 0; from < len(in); from += chunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cl, cr := d.Process(in[from:min(from+chunk, len(in))])
		l, r = append(l, cl...), append(r, cr...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cl, cr := d.Flush()
	return [][]float32{append(l, cl...), append(r, cr...)}, nil
}

// convolveFIR は短いFIR同士の直接畳み込み(長さ len(a)+len(b)-1)。
func convolveFIR(a, b []float32) []float32 {
	out := make([]float32, len(a)+len(b)-1)
	for i, av := range a {
		for j, bv := range b {
			out[i+j] += av * bv
		}
	}
	return out
}

// SubStream はサブウーファーの信号(低域のモノ)に、距離減衰と伝搬遅延を掛ける処理器(Sub の流し処理版)。
// 低域は方向の手がかりが弱く、空気吸収も受けにくいので、HRIRも空気吸収も通さない(両耳に同じ信号を足す)。
// 出力は入力と同じ速さで出てきて(最初の Process で遅延ぶんのゼロが先に付く)、尾はない。
type SubStream struct {
	pad     int
	g       float32
	started bool
	out     []float32
}

// NewSubStream は、距離 dist(m)・追加の遅延 extraDelay(サンプル)のサブの処理器を返す。
func NewSubStream(sr int, dist float64, extraDelay int, rolloff float64) *SubStream {
	return &SubStream{pad: DelaySamples(dist, sr) + max(extraDelay, 0), g: float32(Gain(dist, rolloff))}
}

// Process は入力 in の続きを与え、出力を返す(戻り値は次の Process まで有効)。
func (s *SubStream) Process(in []float32) []float32 {
	n := len(in)
	if !s.started {
		n += s.pad
	}
	if cap(s.out) < n {
		s.out = make([]float32, n)
	}
	out := s.out[:n]
	off := 0
	if !s.started {
		clear(out[:s.pad])
		off = s.pad
		s.started = true
	}
	for i, v := range in {
		out[off+i] = v * s.g
	}
	return out
}

// Sub はサブウーファーの信号(低域のモノ)に、距離減衰と伝搬遅延を掛けて返す。
// 低域は方向の手がかりが弱く、空気吸収も受けにくいので、HRIRも空気吸収も通さない(両耳に同じ信号を足す)。
// 遅延は、リスナーまでの伝搬遅延(dist / 音速)に、サブをメインに時間合わせするための追加の遅延 extraDelay
// (サンプル)を足したもの。追加の遅延は、現場と同じく基準点で1回だけ決める(SubAlignDelays)ので、
// 基準点から離れた席では、サブとメインの時間差やサブ同士の干渉が実際のように出る。
// 減衰はサブ自身の距離で決まる。出力長は len(in)+遅延。SubStream に全体を流す包み。
func Sub(in []float32, sr int, dist float64, extraDelay int, rolloff float64) []float32 {
	return append([]float32(nil), NewSubStream(sr, dist, extraDelay, rolloff).Process(in)...)
}

// SubAlignDelays は、サブをメインに時間合わせするための、サブごとの追加の遅延(サンプル)。
// 基準点(現場のFOHなど、ディレイ補正を取る位置)で、サブの音がメイン(の平均距離)の音と同時に届くように、
// 基準点でサブのほうが近いぶんだけ遅らせる(サブのほうが遠いときは 0)。基準点を1つに固定するので、
// 座席を動かしても追加の遅延は変わらない。
func SubAlignDelays(ref [3]float64, mains, subs [][3]float64, sr int) []int {
	dist := func(p [3]float64) float64 {
		return math.Sqrt((p[0]-ref[0])*(p[0]-ref[0]) + (p[1]-ref[1])*(p[1]-ref[1]) + (p[2]-ref[2])*(p[2]-ref[2]))
	}
	mean := 0.0
	for _, m := range mains {
		mean += dist(m) / float64(len(mains))
	}
	out := make([]int, len(subs))
	for i, s := range subs {
		out[i] = DelaySamples(math.Max(mean-dist(s), 0), sr)
	}
	return out
}
