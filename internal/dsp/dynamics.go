package dsp

import "math"

// CompParams はコンプレッサーのパラメーター(Projectの pa.comp* から渡される)。
type CompParams struct {
	ThresholdDb float64
	Ratio       float64
	AttackMs    float64
	ReleaseMs   float64
}

// Compressor はフィードフォワード式のステレオリンクコンプ(ハードニー、メイクアップなし)。
// ゲインリダクションをdB領域で平滑化する。ゲインリダクションの状態を持つので、信号を区切って順に
// Process してよい(区切り方に結果は依らない)。
type Compressor struct {
	on         bool
	att, rel   float64
	slope, thr float64
	gr         float64 // 現在のゲインリダクション(dB, 正)
}

// NewCompressor はコンプレッサーを返す。Ratio が 1 以下なら Process は何もしない。
func NewCompressor(sr int, p CompParams) *Compressor {
	coef := func(ms float64) float64 {
		return math.Exp(-1 / (math.Max(ms, 0.01) * 1e-3 * float64(sr)))
	}
	return &Compressor{
		on:  p.Ratio > 1,
		att: coef(p.AttackMs), rel: coef(p.ReleaseMs),
		slope: 1 - 1/p.Ratio, thr: p.ThresholdDb,
	}
}

// Process は buf(チャンネル別)にその場でコンプを掛ける。
func (c *Compressor) Process(buf [][]float32) {
	if len(buf) == 0 || !c.on {
		return
	}
	n := len(buf[0])
	for i := 0; i < n; i++ {
		peak := 0.0
		for _, ch := range buf {
			peak = math.Max(peak, math.Abs(float64(ch[i])))
		}
		target := math.Max(LinToDb(peak)-c.thr, 0) * c.slope
		if target > c.gr {
			c.gr = c.att*c.gr + (1-c.att)*target
		} else {
			c.gr = c.rel*c.gr + (1-c.rel)*target
		}
		g := float32(DbToLin(-c.gr))
		for _, ch := range buf {
			ch[i] *= g
		}
	}
}

// Compress は buf 全体にコンプをその場で掛ける。
func Compress(buf [][]float32, sr int, p CompParams) { NewCompressor(sr, p).Process(buf) }

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
