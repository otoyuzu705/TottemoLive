package spatial

import (
	"math"

	"tottemolive/internal/dsp"
)

// 合成HRIR(球形の頭部モデル)。実測HRIRを同梱できるまでの既定セットで、ライセンスの問題がない。
// 両耳間時間差はWoodworthの式、頭部の影は耳との位置関係で変わる低域通過とレベル差で作る。
// 個人差や耳介の効果は持たないので、上下・前後の定位は実測HRIRより弱い。
const (
	synTaps       = 192
	synBaseDelay  = 16     // 小数遅延のため全体に足す遅延(サンプル)
	synHeadRadius = 0.0875 // m
	synIldDb      = 3.0    // 同側 +、反対側 − の最大レベル差
	synIpsiHz     = 18000.0
	synContraHz   = 2000.0
	synRearScale  = 0.7 // 真後ろのカットオフ倍率(耳介の影)
	synAzStepDeg  = 5.0
	synElStepDeg  = 10.0
)

type synthetic struct {
	sr     int
	shadow float64 // spatial.headShadow: 頭の影の強さ(1 でここに書いた値、0 で影なし)
	grid   hrirGrid
}

func newSynthetic(sr int, shadow float64) *synthetic {
	return &synthetic{sr: sr, shadow: shadow}
}

func (s *synthetic) Name() string { return SyntheticName }

func (s *synthetic) Lookup(azDeg, elDeg float64) HRIR {
	return s.grid.lookup(azDeg, elDeg, s.build)
}

func (s *synthetic) build(azDeg, elDeg float64) HRIR {
	az := azDeg * math.Pi / 180
	sinLat, itd := lateral(azDeg, elDeg)
	front := 0.85 + 0.15*math.Cos(az) // 前 1.0、後ろ 0.7

	ear := func(sign float64) []float32 {
		s1 := sign * sinLat // 耳側に向くほど +1
		delay := float64(synBaseDelay)
		if s1 < 0 {
			delay += itd * float64(s.sr)
		}
		h := delayedImpulse(delay)
		fc := synIpsiHz * math.Pow(synContraHz/synIpsiHz, (1-s1)/2*s.shadow) * front
		dsp.LowPass(float64(s.sr), fc).Process(h)
		g := float32(dsp.DbToLin(synIldDb * s1 * s.shadow))
		for n := range h {
			h[n] *= g
		}
		return h
	}
	return HRIR{L: ear(-1), R: ear(+1)}
}
