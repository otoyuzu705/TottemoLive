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

// SetNames は読み込める同梱HRIRセットの名前。
func SetNames() []string { return []string{"synthetic"} }

// LoadSet はHRIRセットを返す。同梱の実測HRIR(WAV + JSON)は素材ごとの再配布条件を確認してから追加する。
func LoadSet(name string, sr int) (Set, error) {
	switch name {
	case "synthetic":
		return newSynthetic(sr), nil
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

// Direct はスピーカーへの入力 in を、距離減衰・空気吸収・伝搬遅延・HRIR畳み込みを通した
// 左右の耳の信号にして返す。出力長は len(in)+遅延+len(HRIR)-1。
func Direct(ctx context.Context, in []float32, sr int, dist, azDeg, elDeg float64, set Set, p DirectParams) ([][]float32, error) {
	// 空気吸収(線形位相FIR)の群遅延ぶん、伝搬遅延から引いて、全体の遅れを合わせる(近すぎて引けないぶんは遅れる)
	air := AirFIR(dist, p.AirAbsorption, sr)
	delay := DelaySamples(dist, sr)
	lead := 0
	if air != nil {
		lead = min(delay, AirGroupDelay)
	}
	x := make([]float32, delay-lead+len(in))
	g := float32(Gain(dist, p.Rolloff))
	for i, v := range in {
		x[delay-lead+i] = v * g
	}
	h := set.Lookup(azDeg, elDeg)
	hl, hr := h.L, h.R
	if air != nil {
		// 吸収は、距離ごとの小さなFIRなので、HRIRと先に畳み込んで1回の畳み込みにする(長さは512以下に収まる)
		hl, hr = convolveFIR(h.L, air), convolveFIR(h.R, air)
	}
	earL, earR, err := dsp.ConvolvePair(ctx, x, hl, hr)
	if err != nil {
		return nil, err
	}
	return [][]float32{earL, earR}, nil
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

// Sub はサブウーファーの信号(低域のモノ)に、距離減衰と伝搬遅延を掛けて返す。
// 低域は方向の手がかりが弱く、空気吸収も受けにくいので、HRIRも空気吸収も通さない(両耳に同じ信号を足す)。
// 遅延は、リスナーまでの伝搬遅延(dist / 音速)に、サブをメインに時間合わせするための追加の遅延 extraDelay
// (サンプル)を足したもの。追加の遅延は、現場と同じく基準点で1回だけ決める(SubAlignDelays)ので、
// 基準点から離れた席では、サブとメインの時間差やサブ同士の干渉が実際のように出る。
// 減衰はサブ自身の距離で決まる。出力長は len(in)+遅延。
func Sub(in []float32, sr int, dist float64, extraDelay int, rolloff float64) []float32 {
	delay := DelaySamples(dist, sr) + max(extraDelay, 0)
	g := float32(Gain(dist, rolloff))
	out := make([]float32, delay+len(in))
	for i, v := range in {
		out[delay+i] = v * g
	}
	return out
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
