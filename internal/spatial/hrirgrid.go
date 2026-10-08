package spatial

import (
	"math"
	"sync"
)

// HRIRの方向の量子化(方位 synAzStepDeg・仰角 synElStepDeg)と、量子化した方向ごとの保持。
// 合成(synthetic)と球形頭部モデル(brownDuda)が共有する。
type hrirGrid struct {
	mu    sync.Mutex
	cache map[[2]int]HRIR
}

// lookup は (azDeg, elDeg) を量子化し、無ければ build で作って保持する。
func (g *hrirGrid) lookup(azDeg, elDeg float64, build func(azDeg, elDeg float64) HRIR) HRIR {
	azDeg = math.Mod(azDeg+540, 360) - 180 // -180〜180
	ka := int(math.Round(azDeg / synAzStepDeg))
	ke := int(math.Round(elDeg / synElStepDeg))
	key := [2]int{ka, ke}
	g.mu.Lock()
	defer g.mu.Unlock()
	if h, ok := g.cache[key]; ok {
		return h
	}
	h := build(float64(ka)*synAzStepDeg, float64(ke)*synElStepDeg)
	if g.cache == nil {
		g.cache = map[[2]int]HRIR{}
	}
	g.cache[key] = h
	return h
}

// delayedImpulse は delay(サンプル、小数可)だけ遅れた、窓付きsincのインパルス(長さ synTaps)。
func delayedImpulse(delay float64) []float32 {
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
	return h
}

// lateral は方向の耳軸への射影 sinLat(+1 が真右)と、Woodworth の両耳間時間差(秒)。
func lateral(azDeg, elDeg float64) (sinLat, itdSec float64) {
	az, el := azDeg*math.Pi/180, elDeg*math.Pi/180
	sinLat = math.Sin(az) * math.Cos(el)
	lat := math.Asin(math.Max(-1, math.Min(1, sinLat)))
	itdSec = synHeadRadius / SpeedOfSound * (math.Abs(lat) + math.Abs(sinLat))
	return
}
