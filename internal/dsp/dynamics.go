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
	work       []float64 // Process の作業用(1ブロックぶん。target → ゲインリダクションの順に使い回す)
}

// コンプを掛けるときの1ブロックのサンプル数(作業用配列の大きさ)と、並列化するときの1区間の最小サンプル数。
const (
	compBlock  = 65536
	compMinPer = 1024
)

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
//
// サンプルごとの処理を3段に分ける。状態を持つのは2段目(ゲインリダクション gr の漸化式)だけなので、
// 1・3段目はサンプル方向に並列に回す(各サンプルは自分の値だけで決まる)。式は元の1重ループと同じなので、
// 並列度・区切り方に依らず結果はビット単位で同じ。
//
//	(1) 各サンプルの peak → LinToDb → target(並列)
//	(2) target から gr を順に求める(直列。軽い)
//	(3) DbToLin(-gr) を各チャンネルに掛ける(並列)
func (c *Compressor) Process(buf [][]float32) {
	if len(buf) == 0 || !c.on {
		return
	}
	n := len(buf[0])
	if len(c.work) < min(n, compBlock) {
		c.work = make([]float64, min(n, compBlock))
	}
	for from := 0; from < n; from += compBlock {
		m := min(compBlock, n-from)
		tg := c.work[:m]
		parallelFor(m, compMinPer, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				peak := 0.0
				for _, ch := range buf {
					peak = math.Max(peak, math.Abs(float64(ch[from+i])))
				}
				tg[i] = math.Max(LinToDb(peak)-c.thr, 0) * c.slope
			}
		})
		for i, target := range tg {
			if target > c.gr {
				c.gr = c.att*c.gr + (1-c.att)*target
			} else {
				c.gr = c.rel*c.gr + (1-c.rel)*target
			}
			tg[i] = c.gr
		}
		parallelFor(m, compMinPer, func(lo, hi int) {
			for i := lo; i < hi; i++ {
				g := float32(DbToLin(-tg[i]))
				for _, ch := range buf {
					ch[from+i] *= g
				}
			}
		})
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
	if len(buf) == 0 {
		return
	}
	// 各サンプルは状態を持たないので、サンプル方向に並列に回してよい(結果は変わらない)
	parallelFor(len(buf[0]), satMinPer, func(lo, hi int) {
		for _, ch := range buf {
			for i := lo; i < hi; i++ {
				ch[i] = float32(math.Tanh(k*float64(ch[i])) / k)
			}
		}
	})
}

// Saturate を並列化するときの1区間の最小サンプル数。
const satMinPer = 4096
