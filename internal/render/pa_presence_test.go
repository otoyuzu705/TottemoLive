package render

import (
	"math/rand"
	"testing"

	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

// refApplyPAPresence は legacyApplyPA の低域シェルフと高域シェルフの間に、プレゼンス(ピーキング)を入れた写し。
func refApplyPAPresence(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	g := float32(dsp.DbToLin(gainDb))
	for _, ch := range buf {
		for i := range ch {
			ch[i] *= g
		}
		dsp.HighPass(float64(sr), pa.LowCutHz).Process(ch)
		dsp.LowShelf(float64(sr), pa.LowShelfHz, pa.LowShelfDb).Process(ch)
		dsp.Peaking(float64(sr), pa.PresenceHz, pa.PresenceDb, pa.PresenceQ).Process(ch)
		dsp.HighShelf(float64(sr), pa.HighShelfHz, pa.HighShelfDb).Process(ch)
	}
	dsp.Compress(buf, sr, dsp.CompParams{
		ThresholdDb: pa.CompThresholdDb, Ratio: pa.CompRatio,
		AttackMs: pa.CompAttackMs, ReleaseMs: pa.CompReleaseMs,
	})
	dsp.Saturate(buf, pa.Drive)
}

// プレゼンスが有効なときは、低域シェルフと高域シェルフの間にピーキングを入れた処理と、チャンクの分け方に依らず一致する。
// 量 0 のときは周波数・Q を変えても従来のPA(legacyApplyPA)と一致する。
func TestPAPresence(t *testing.T) {
	rng := rand.New(rand.NewSource(71))
	pa := project.New().PA
	pa.PresenceDb, pa.PresenceHz, pa.PresenceQ = 4, 3500, 1.5
	zero := project.New().PA
	zero.PresenceHz, zero.PresenceQ = 5000, 2.5
	for _, n := range []int{1, 1000, 30001} {
		src := randStereo(rng, n, 0.8)
		want, wantZero := cloneStereo(src), cloneStereo(src)
		refApplyPAPresence(want, sampleRate, -3.5, pa)
		legacyApplyPA(wantZero, sampleRate, -3.5, zero)
		for _, size := range []int{1, 7, 997, 65536} {
			if size == 1 && n > 1000 {
				continue
			}
			for _, c := range []struct {
				name string
				pa   project.PA
				want [][]float32
			}{{"presence 4 dB", pa, want}, {"presence 0", zero, wantZero}} {
				got := cloneStereo(src)
				proc := newPAProc(sampleRate, -3.5, c.pa)
				for from := 0; from < n; from += size {
					proc.Process(sliceBus(got, from, min(from+size, n)))
				}
				if m, d := compareAudioQuiet(got, c.want); d > 0 {
					t.Fatalf("%s n=%d chunk=%d: %d samples differ (max %g)", c.name, n, size, d, m)
				}
			}
		}
	}
}
