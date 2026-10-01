package spatial

import (
	"math"
	"sync"

	"livebin/internal/dsp"
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
	sr    int
	mu    sync.Mutex
	cache map[[2]int]HRIR
}

func newSynthetic(sr int) *synthetic {
	return &synthetic{sr: sr, cache: map[[2]int]HRIR{}}
}

func (s *synthetic) Name() string { return "synthetic" }

func (s *synthetic) Lookup(azDeg, elDeg float64) HRIR {
	azDeg = math.Mod(azDeg+540, 360) - 180 // -180〜180
	ka := int(math.Round(azDeg / synAzStepDeg))
	ke := int(math.Round(elDeg / synElStepDeg))
	key := [2]int{ka, ke}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.cache[key]; ok {
		return h
	}
	h := s.build(float64(ka)*synAzStepDeg, float64(ke)*synElStepDeg)
	s.cache[key] = h
	return h
}

func (s *synthetic) build(azDeg, elDeg float64) HRIR {
	az, el := azDeg*math.Pi/180, elDeg*math.Pi/180
	sinLat := math.Sin(az) * math.Cos(el) // 右耳軸への射影(+1 が真右)
	lat := math.Asin(math.Max(-1, math.Min(1, sinLat)))
	itd := synHeadRadius / SpeedOfSound * (math.Abs(lat) + math.Abs(sinLat))
	front := 0.85 + 0.15*math.Cos(az) // 前 1.0、後ろ 0.7

	ear := func(sign float64) []float32 {
		s1 := sign * sinLat // 耳側に向くほど +1
		delay := float64(synBaseDelay)
		if s1 < 0 {
			delay += itd * float64(s.sr)
		}
		h := make([]float32, synTaps)
		for n := range h {
			t := float64(n) - delay
			v := 1.0
			if t != 0 {
				v = math.Sin(math.Pi*t) / (math.Pi * t)
			}
			w := 0.5 + 0.5*math.Cos(math.Pi*t/synBaseDelay)
			if math.Abs(t) > synBaseDelay {
				w = 0
			}
			h[n] = float32(v * w)
		}
		fc := synIpsiHz * math.Pow(synContraHz/synIpsiHz, (1-s1)/2) * front
		dsp.LowPass(float64(s.sr), fc).Process(h)
		g := float32(dsp.DbToLin(synIldDb * s1))
		for n := range h {
			h[n] *= g
		}
		return h
	}
	return HRIR{L: ear(-1), R: ear(+1)}
}
