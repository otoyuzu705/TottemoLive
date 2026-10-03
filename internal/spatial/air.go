package spatial

import (
	"math"

	"gonum.org/v1/gonum/dsp/fourier"
)

// 空気吸収(ISO 9613-1)。空気が高域を吸収する量は、距離に比例して dB が増え、周波数のほぼ2乗で大きくなる。
// 大気の条件は、標準的な 20 ℃・相対湿度 50 %・1気圧。spatial.airAbsorption は、この物理値に対する倍率
// (1 で物理値、0 で吸収なし)。
const (
	airTempK     = 293.15
	airHumidity  = 50.0 // 相対湿度(%)
	airMaxAttnDb = 80.0 // これ以上は減衰させない(FIRのダイナミックレンジの限界)

	// airTaps は吸収フィルタ(線形位相FIR)のタップ数。群遅延は (airTaps-1)/2 サンプル。
	airTaps = 255
	// airGrid は周波数サンプリングで設計するときのFFT長。
	airGrid = 2048
)

// AirGroupDelay は吸収フィルタの群遅延(サンプル)。Direct はこの分だけ伝搬遅延から引いて、全体の遅れを合わせる。
const AirGroupDelay = (airTaps - 1) / 2

// AirAbsorptionDbPerM は周波数 f(Hz)の純音が空気中を 1 m 進むときの吸収(dB/m)。ISO 9613-1 の式
// (20 ℃・相対湿度 50 %・1気圧)。1 kHz で約 0.005、4 kHz で約 0.02、8 kHz で約 0.08、16 kHz で約 0.3 dB/m。
func AirAbsorptionDbPerM(f float64) float64 {
	const t0, t01 = 293.15, 273.16
	t := airTempK
	// 水蒸気のモル濃度 h(%): 飽和蒸気圧と相対湿度から
	psat := math.Pow(10, -6.8346*math.Pow(t01/t, 1.261)+4.6151) // 圧力比 psat/pr
	h := airHumidity * psat
	frO := 24 + 4.04e4*h*(0.02+h)/(0.391+h) // 酸素の緩和周波数
	frN := math.Pow(t/t0, -0.5) * (9 + 280*h*math.Exp(-4.170*(math.Pow(t/t0, -1.0/3)-1)))
	f2 := f * f
	return 8.686 * f2 * (1.84e-11*math.Sqrt(t/t0) +
		math.Pow(t/t0, -2.5)*(0.01275*math.Exp(-2239.1/t)/(frO+f2/frO)+0.1068*math.Exp(-3352/t)/(frN+f2/frN)))
}

// AirFIR は、距離 dist(m)を進んだ音が空気に吸収される高域の減衰を表す線形位相FIR(長さ airTaps、直流ゲイン1)。
// 各周波数の減衰は scale × α(f) × dist(dB。scale=1 で物理値)で、周波数サンプリングで設計して窓を掛ける。
// dist または scale が 0 以下なら nil(吸収なし)。
func AirFIR(dist, scale float64, sr int) []float32 {
	if dist <= 0 || scale <= 0 {
		return nil
	}
	// 周波数応答(振幅)を、0〜ナイキストの片側のbinに並べる。位相はゼロ(あとで中心にずらして線形位相にする)
	spec := make([]complex128, airGrid/2+1)
	for k := range spec {
		attn := math.Min(scale*AirAbsorptionDbPerM(float64(k)*float64(sr)/airGrid)*dist, airMaxAttnDb)
		spec[k] = complex(math.Pow(10, -attn/20), 0)
	}
	imp := fourier.NewFFT(airGrid).Sequence(nil, spec) // ゼロ位相のインパルス応答(循環)。正規化は airGrid で割る
	h := make([]float32, airTaps)
	c := AirGroupDelay
	var sum float64
	for i := range h {
		n := ((i-c)%airGrid + airGrid) % airGrid                   // 中心(0)の前後
		w := 0.5 + 0.5*math.Cos(math.Pi*float64(i-c)/float64(c+1)) // Hann窓
		v := imp[n] / airGrid * w
		h[i] = float32(v)
		sum += v
	}
	if sum > 0 { // 窓による直流ゲインのずれを戻す(低域の音量を変えない)
		for i := range h {
			h[i] = float32(float64(h[i]) / sum)
		}
	}
	return h
}
