package dsp

import "math"

// CompParams はコンプレッサーのパラメーター(Projectの pa.comp* から渡される)。
type CompParams struct {
	ThresholdDb float64
	Ratio       float64
	AttackMs    float64
	ReleaseMs   float64
}

// Compress はフィードフォワード式のステレオリンクコンプ(ハードニー、メイクアップなし)をその場で掛ける。
// ゲインリダクションをdB領域で平滑化する。
func Compress(buf [][]float32, sr int, p CompParams) {
	if len(buf) == 0 || p.Ratio <= 1 {
		return
	}
	coef := func(ms float64) float64 {
		return math.Exp(-1 / (math.Max(ms, 0.01) * 1e-3 * float64(sr)))
	}
	att, rel := coef(p.AttackMs), coef(p.ReleaseMs)
	slope := 1 - 1/p.Ratio
	gr := 0.0 // 現在のゲインリダクション(dB, 正)
	n := len(buf[0])
	for i := 0; i < n; i++ {
		peak := 0.0
		for _, ch := range buf {
			peak = math.Max(peak, math.Abs(float64(ch[i])))
		}
		target := math.Max(LinToDb(peak)-p.ThresholdDb, 0) * slope
		if target > gr {
			gr = att*gr + (1-att)*target
		} else {
			gr = rel*gr + (1-rel)*target
		}
		g := float32(DbToLin(-gr))
		for _, ch := range buf {
			ch[i] *= g
		}
	}
}

// driveMaxK は drive=1 のときの tanh の入力ゲイン。方式の形であり、強さは pa.drive で決まる。
const driveMaxK = 6.0

// Saturate は tanh による歪みをその場で掛ける。y = tanh(k·x)/k で、小信号のゲインは1のまま。
// drive=0 は何もしない。
func Saturate(buf [][]float32, drive float64) {
	if drive <= 0 {
		return
	}
	k := 1 + drive*(driveMaxK-1)
	for _, ch := range buf {
		for i, v := range ch {
			ch[i] = float32(math.Tanh(k*float64(v)) / k)
		}
	}
}
