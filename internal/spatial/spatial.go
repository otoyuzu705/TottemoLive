// Package spatial はHRIRの読み込み、距離モデル、仮想スピーカーからリスナーへの直接音の計算を持つ。
// 座標はステージ中央を原点としたメートル単位(x 右、y 客席側、z 高さ)。
package spatial

import (
	"context"
	"fmt"
	"math"

	"livebin/internal/dsp"
)

// 距離モデルの形を決める定数。強さそのものは Project の spatial.* で調整する。
const (
	SpeedOfSound = 343.0 // m/s
	// RefDistanceM は距離減衰の基準距離。これより近い音は減衰せず、最大 MaxGain まで持ち上げる。
	RefDistanceM = 10.0
	MaxGain      = 4.0
	// 空気吸収: カットオフ = AirBaseHz / (1 + absorption * d / AirScaleM)、AirMinHz を下限にする。
	AirBaseHz = 20000.0
	AirScaleM = 40.0
	AirMinHz  = 1500.0
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

// Gain は距離 d(m) での減衰ゲイン。rolloff=1 で逆距離則。
func Gain(d, rolloff float64) float64 {
	if d <= RefDistanceM {
		return 1
	}
	return math.Min(math.Pow(RefDistanceM/d, rolloff), MaxGain)
}

// AirCutoffHz は空気吸収を模す低域通過のカットオフ。absorption=0 のときは 0(フィルタなし)。
func AirCutoffHz(d, absorption float64) float64 {
	if absorption <= 0 {
		return 0
	}
	return math.Max(AirBaseHz/(1+absorption*d/AirScaleM), AirMinHz)
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
	delay := DelaySamples(dist, sr)
	x := make([]float32, delay+len(in))
	g := float32(Gain(dist, p.Rolloff))
	for i, v := range in {
		x[delay+i] = v * g
	}
	if fc := AirCutoffHz(dist, p.AirAbsorption); fc > 0 {
		dsp.LowPass(float64(sr), fc).Process(x)
	}
	h := set.Lookup(azDeg, elDeg)
	earL, earR, err := dsp.ConvolvePair(ctx, x, h.L, h.R)
	if err != nil {
		return nil, err
	}
	return [][]float32{earL, earR}, nil
}

// Sub はサブウーファーの信号(低域のモノ)に、距離減衰と伝搬遅延を掛けて返す。
// 低域は方向の手がかりが弱く、空気吸収も受けにくいので、HRIRも空気吸収も通さない(両耳に同じ信号を足す)。
// 実際のPAと同じく、サブとメインの音がリスナーで揃うように遅延をそろえる:
// 遅延は サブ自身の距離とメインの代表距離 alignDist の長いほうに合わせる(サブのほうが近いときだけサブを遅らせる)。
// 減衰はサブ自身の距離で決まる。出力長は len(in)+遅延。
func Sub(in []float32, sr int, dist, alignDist, rolloff float64) []float32 {
	delay := DelaySamples(math.Max(dist, alignDist), sr)
	g := float32(Gain(dist, rolloff))
	out := make([]float32, delay+len(in))
	for i, v := range in {
		out[delay+i] = v * g
	}
	return out
}
