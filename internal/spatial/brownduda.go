package spatial

import (
	"math"

	"tottemolive/internal/dsp"
)

// Brown–Duda(1998)の球形頭部モデル。頭の影を、耳ごとに1極1零のシェルフ(低域はそのまま、高域は耳軸からの入射角で
// +6 dB〜−20 dB)で表す。両耳間時間差は合成(synthetic)と同じWoodworthの式。耳介の効果と前後の違いは持たない。
const (
	bdAlphaMin    = 0.1   // 影がいちばん深い向きでの高域ゲイン(−20 dB)。モデルの形の定数(強さは HeadShadow)
	bdThetaMinDeg = 150.0 // 影がいちばん深い入射角(耳軸から)
)

// bdAlpha は耳軸からの入射角 thetaDeg(0 = 耳の真横から、180 = 反対側の真横から)での、高域のゲイン(線形)。
func bdAlpha(thetaDeg float64) float64 {
	return (1 + bdAlphaMin/2) + (1-bdAlphaMin/2)*math.Cos(thetaDeg/bdThetaMinDeg*math.Pi)
}

type brownDuda struct {
	sr     int
	shadow float64 // spatial.headShadow: α を α^shadow にする(1 でモデルの値、0 で影なし)
	grid   hrirGrid
}

func newBrownDuda(sr int, shadow float64) *brownDuda {
	return &brownDuda{sr: sr, shadow: shadow}
}

func (s *brownDuda) Name() string { return BrownDudaName }

func (s *brownDuda) Lookup(azDeg, elDeg float64) HRIR {
	return s.grid.lookup(azDeg, elDeg, s.build)
}

func (s *brownDuda) build(azDeg, elDeg float64) HRIR {
	sinLat, itd := lateral(azDeg, elDeg)
	shelfHz := SpeedOfSound / (math.Pi * synHeadRadius) // 2ω0/(2π)、約1248 Hz
	ear := func(sign float64) []float32 {
		s1 := sign * sinLat // 耳側に向くほど +1
		delay := float64(synBaseDelay)
		if s1 < 0 {
			delay += itd * float64(s.sr)
		}
		h := delayedImpulse(delay)
		theta := math.Acos(math.Max(-1, math.Min(1, s1))) * 180 / math.Pi // 耳軸からの入射角
		dsp.FirstOrderShelf(float64(s.sr), shelfHz, math.Pow(bdAlpha(theta), s.shadow)).Process(h)
		return h
	}
	return HRIR{L: ear(-1), R: ear(+1)}
}
