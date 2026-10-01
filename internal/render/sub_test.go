package render

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"

	"tottemolive/internal/audio"
	"tottemolive/internal/project"
)

// 60 Hz(サブの帯域)と 1 kHz(メインの帯域)の正弦波を重ねた4秒の音源を作る。
func makeBassSource(t *testing.T) string {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(t.TempDir(), "bass.wav")
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=60:duration=4:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=4:sample_rate=48000",
		"-filter_complex", "[0]volume=0.5[a];[1]volume=0.2[b];[a][b]amix=inputs=2:normalize=0", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

// tonePower は f Hz の成分のパワー(Hann窓のDFT、左チャンネル、先頭と末尾の1秒を除く)。
func tonePower(x []float32, f float64) float64 {
	from, to := 48000, len(x)-48000
	var re, im float64
	n := float64(to - from)
	for i := from; i < to; i++ {
		w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i-from)/n)
		ph := 2 * math.Pi * f * float64(i) / 48000
		re += float64(x[i]) * w * math.Cos(ph)
		im -= float64(x[i]) * w * math.Sin(ph)
	}
	return re*re + im*im
}

func bassRatioDb(t *testing.T, p project.Project) float64 {
	t.Helper()
	res, err := Render(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	return 10 * math.Log10(tonePower(res.Audio[0], 60)/tonePower(res.Audio[0], 1000))
}

func bassProject(src string) project.Project {
	p := project.New()
	p.Sources = []project.Source{{ID: "s", Path: src, Role: project.RoleMix}}
	p.Venue = project.DefaultVenue()
	p.Reverb.Mix = 0 // 純音は、ノイズ状の残響IRを通ると耳ごとにランダムな位相・振幅になるので、直接音だけで比べる
	p.Crowd.Density = 0
	p.PA.Drive = 0
	p.PA.CompRatio = 1 // 帯域の比を見るので、非線形な段は外す
	return p
}

// サブのレベルを上げると、低域(60 Hz)が中高域(1 kHz)に対してその分だけ増える。
// サブ 0 dB は、サブなしのメインの低域とほぼ同じ大きさ。
func TestSubLevelRaisesBass(t *testing.T) {
	p := bassProject(makeBassSource(t))
	off := p.Clone()
	off.Sub.Enabled = "off"
	r0, r9 := p.Clone(), p.Clone()
	r0.Sub.LevelDb, r9.Sub.LevelDb = 0, 9
	ratioOff, ratio0, ratio9 := bassRatioDb(t, off), bassRatioDb(t, r0), bassRatioDb(t, r9)
	t.Logf("60Hz/1kHz: off %.1f dB, sub 0 dB %.1f dB, sub +9 dB %.1f dB", ratioOff, ratio0, ratio9)
	if d := ratio9 - ratio0; math.Abs(d-9) > 1.5 {
		t.Errorf("+9 dB of sub level raised the bass by %.1f dB", d)
	}
	if d := ratio0 - ratioOff; math.Abs(d) > 2.5 {
		t.Errorf("sub at 0 dB should be close to the mains' low end, differs by %.1f dB", d)
	}
}

// サブが無効のとき、またはサブが1台も無いときは、サブ経路は何もしない(結果が完全に同じ)。
func TestSubOffEqualsNoSubs(t *testing.T) {
	p := bassProject(makeBassSource(t))
	off := p.Clone()
	off.Sub.Enabled = "off"
	none := p.Clone()
	none.Venue.Subs = nil
	a, err := Render(context.Background(), off, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(context.Background(), none, nil)
	if err != nil {
		t.Fatal(err)
	}
	for c := range a.Audio {
		for i := range a.Audio[c] {
			if a.Audio[c][i] != b.Audio[c][i] {
				t.Fatalf("differs at ch%d[%d]", c, i)
			}
		}
	}
}

// サブの位置を左右で動かしても(近いほうが大きい)、低域は両耳に同じ信号として入る。
func TestSubIsMonoInBothEars(t *testing.T) {
	p := bassProject(makeBassSource(t))
	p.Venue.Speakers = []project.Speaker{{ID: "L", X: 0, Y: 0, Z: 8}} // メイン1本(中央)なら左右対称
	p.Listener.X = 0
	p.PA.LowCutHz = 20
	res, err := Render(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	l, r := tonePower(res.Audio[0], 60), tonePower(res.Audio[1], 60)
	if d := math.Abs(10 * math.Log10(l/r)); d > 0.5 {
		t.Errorf("60 Hz differs between ears by %.2f dB", d)
	}
}
