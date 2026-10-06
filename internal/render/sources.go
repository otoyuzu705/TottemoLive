package render

import (
	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

// paProc は音源1本ぶんの(ゲイン → PA質感)の処理器。フィルタ・コンプの状態を持つので、
// 音源を区切って順に Process してよい(結果は区切り方に依らない)。
type paProc struct {
	g     float32
	f     [2][3]*dsp.Biquad // チャンネルごとの 低域カット → 低域シェルフ → 高域シェルフ
	comp  *dsp.Compressor
	drive float64
}

// newPAProc は、音源ゲイン(+レベル合わせ)gainDb と pa.* の設定の処理器を返す。
func newPAProc(sr int, gainDb float64, pa project.PA) *paProc {
	p := &paProc{
		g: float32(dsp.DbToLin(gainDb)),
		comp: dsp.NewCompressor(sr, dsp.CompParams{
			ThresholdDb: pa.CompThresholdDb, Ratio: pa.CompRatio,
			AttackMs: pa.CompAttackMs, ReleaseMs: pa.CompReleaseMs,
		}),
		drive: pa.Drive,
	}
	for c := range p.f {
		p.f[c] = [3]*dsp.Biquad{
			dsp.HighPass(float64(sr), pa.LowCutHz),
			dsp.LowShelf(float64(sr), pa.LowShelfHz, pa.LowShelfDb),
			dsp.HighShelf(float64(sr), pa.HighShelfHz, pa.HighShelfDb),
		}
	}
	return p
}

// Process は buf(ステレオ)にその場でゲインとPA質感(低域カット → 低域シェルフ → 高域シェルフ → コンプ → 歪み)を掛ける。
func (p *paProc) Process(buf [][]float32) {
	for c, ch := range buf {
		for i := range ch {
			ch[i] *= p.g
		}
		for _, f := range p.f[c] {
			f.Process(ch)
		}
	}
	p.comp.Process(buf)
	dsp.Saturate(buf, p.drive)
}

// applyPA は音源にゲインを掛けてPA質感(低域カット → 低域シェルフ → 高域シェルフ → コンプ → 歪み)を付ける。
// paProc に全体を渡す包み。
func applyPA(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	newPAProc(sr, gainDb, pa).Process(buf)
}
