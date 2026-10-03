package render

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"

	"tottemolive/internal/analysis"
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

// プレビューの結果には、PA出力の帯域レベルが入る。書き出し(Render)には入らない。
func TestPreviewHasPASpectrum(t *testing.T) {
	p := bassProject(makeBassSource(t))
	e := NewEngine()
	res, err := e.Preview(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	pa := res.PA
	if pa == nil || pa.Series == nil || pa.Series.Bands != analysis.NumBands() || pa.Series.Frames < 70 {
		t.Fatalf("PA spectrum missing or wrong shape: %+v", pa)
	}
	// 音源は 60 Hz(0.5)と 1 kHz(0.2): PA出力でも 60 Hz のほうが約8 dB大きく、2 kHz以上はほとんど無い
	mid := pa.Series.Frames / 2
	row := pa.Series.Data[mid*pa.Series.Bands : (mid+1)*pa.Series.Bands]
	idx := func(fc float64) int {
		for i, c := range analysis.BandCenters {
			if c == fc {
				return i
			}
		}
		return -1
	}
	if d := float64(row[idx(63)] - row[idx(1000)]); d < 4 || d > 12 {
		t.Errorf("63 Hz vs 1 kHz in PA output: %.1f dB (want ~8)", d)
	}
	if row[idx(4000)] > -50 {
		t.Errorf("4 kHz band should be nearly empty: %.1f dB", row[idx(4000)])
	}
	if math.IsNaN(pa.OffsetDb) || math.IsInf(pa.OffsetDb, 0) {
		t.Errorf("offset %v", pa.OffsetDb)
	}

	// 書き出し用の Render には入れない(余計な計算をしない)
	full, err := Render(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.PA != nil {
		t.Error("Render should not compute the PA spectrum")
	}
}

// 残響量や座席を変えても、PA出力の帯域レベルは再計算されない(同じ結果が返る)。PAを変えると変わる。
func TestPASpectrumCaching(t *testing.T) {
	p := bassProject(makeBassSource(t))
	e := NewEngine()
	first, err := e.Preview(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := p.Clone()
	q.Reverb.Mix = 0.5
	q.Listener.X = 4
	second, err := e.Preview(context.Background(), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.PA.Series != second.PA.Series {
		t.Error("PA spectrum was recomputed although the PA did not change")
	}
	r := p.Clone()
	r.PA.LowShelfDb = 9
	third, err := e.Preview(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if third.PA.Series == first.PA.Series {
		t.Error("PA spectrum should change when the PA changes")
	}
	i := 5 // 63 Hz 帯
	mid := first.PA.Series.Frames / 2
	if a, b := third.PA.Series.Data[mid*31+i], first.PA.Series.Data[mid*31+i]; a-b < 6 {
		t.Errorf("+9 dB low shelf raised the 63 Hz band by only %.1f dB", a-b)
	}
}

// 残響の入力(放射された音)。サブが無効なら左右平均。有効なら メインの高域 + サブの低域×レベル。
func TestRadiatedMono(t *testing.T) {
	const n = 2 * 48000
	mk := func(f float64) [][]float32 {
		x := make([]float32, n)
		for i := range x {
			x[i] = float32(math.Sin(2 * math.Pi * f * float64(i) / 48000))
		}
		return [][]float32{x, append([]float32(nil), x...)} // 左右同じ(中央に定位した音)
	}
	amp := func(x []float32) float64 { // 後半1秒の振幅(RMS×√2)
		s := 0.0
		for _, v := range x[48000:] {
			s += float64(v) * float64(v)
		}
		return math.Sqrt(2 * s / 48000)
	}
	on := func(level float64) project.Sub { return project.Sub{Enabled: "on", LevelDb: level, CrossoverHz: 90} }

	// 無効: 左右平均そのもの
	bus := mk(60)
	bus[1] = make([]float32, n)
	m := radiatedMono(bus, false, project.Sub{})
	for i := range m {
		if m[i] != bus[0][i]/2 {
			t.Fatalf("inactive: [%d] %v want %v", i, m[i], bus[0][i]/2)
		}
	}

	// 有効・サブ 0 dB: 低域(60 Hz)も高域(1 kHz)も、分けない場合(振幅1)とほぼ同じ大きさ
	for _, f := range []float64{60, 1000} {
		if a := amp(radiatedMono(mk(f), true, on(0))); math.Abs(a-1) > 0.15 {
			t.Errorf("%v Hz at sub 0 dB: amplitude %.2f, want ~1", f, a)
		}
	}
	// サブのレベルを +9 dB にすると、低域だけが約 +9 dB(約2.8倍)になり、高域は変わらない
	low0, low9 := amp(radiatedMono(mk(60), true, on(0))), amp(radiatedMono(mk(60), true, on(9)))
	if d := 20 * math.Log10(low9/low0); math.Abs(d-9) > 1 {
		t.Errorf("60 Hz: +9 dB of sub level changed the reverb feed by %.1f dB", d)
	}
	hi0, hi9 := amp(radiatedMono(mk(1000), true, on(0))), amp(radiatedMono(mk(1000), true, on(9)))
	if d := 20 * math.Log10(hi9/hi0); math.Abs(d) > 0.2 {
		t.Errorf("1 kHz moved by %.2f dB", d)
	}
}

// サブ有効時は、サブのレベルを変えると残響も(低域だけ)変わる。無効のときは、サブの設定を変えても何も再計算されない。
func TestSubAffectsReverbOnlyWhenEnabled(t *testing.T) {
	p := bassProject(makeBassSource(t))
	p.Reverb.Mix = 1 // 残響だけを聴く
	p.Spatial.DirectLevelDb = -12
	e := NewEngine()
	counts := func() map[string]int { return computed(e) }
	render := func(q project.Project) {
		if _, err := e.Preview(context.Background(), q, nil); err != nil {
			t.Fatal(err)
		}
	}
	render(p)
	before := counts()
	off := p.Clone()
	off.Sub.Enabled = "off"
	render(off) // 無効にすると、直接音も残響も作り直し
	afterOff := counts()
	if afterOff["reverb"] == before["reverb"] || afterOff["direct"] == before["direct"] {
		t.Error("disabling the sub should recompute direct and reverb")
	}
	off2 := off.Clone()
	off2.Sub.LevelDb = 9
	off2.Sub.CrossoverHz = 120
	off2.Venue.Subs[0].X = -2
	render(off2) // 無効のあいだは、サブの設定・位置を変えても何も再計算しない
	if got := counts(); got["reverb"] != afterOff["reverb"] || got["direct"] != afterOff["direct"] {
		t.Error("sub settings must not matter while the sub is disabled")
	}
}

// 同じ曲を音量だけ変えた2つの音源は、PA入力のレベル合わせがオンなら、ほぼ同じ音になる(プリセットが曲のマスターの音量に依らない)。
// オフだと、コンプ・歪みの効き方が違うので別の音になる。
func TestAutoLevelMakesPAIndependentOfSourceLevel(t *testing.T) {
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	dir := t.TempDir()
	loud, quiet := filepath.Join(dir, "loud.wav"), filepath.Join(dir, "quiet.wav")
	for path, vol := range map[string]string{loud: "1.0", quiet: "0.08"} {
		out, err := exec.Command("ffmpeg", "-v", "error", "-y",
			"-f", "lavfi", "-i", "sine=frequency=100:duration=4:sample_rate=48000",
			"-f", "lavfi", "-i", "sine=frequency=1500:duration=4:sample_rate=48000",
			"-filter_complex", "[0]volume=0.5[a];[1]volume=0.4,tremolo=f=3:d=1[b];[a][b]amix=inputs=2:normalize=0,volume="+vol, path).CombinedOutput()
		if err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	render := func(path, auto string) [][]float32 {
		p := bassProject(path)
		p.PA.AutoLevel = auto
		p.PA.CompThresholdDb = -24
		p.PA.CompRatio = 6
		p.PA.Drive = 0.6 // 入力のレベルで効き方が変わる非線形な段を強めに効かせる
		res, err := Render(context.Background(), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Audio
	}
	relDiffDb := func(a, b [][]float32) float64 { // 左チャンネルの差のRMS / 信号のRMS(dB)
		n := min(len(a[0]), len(b[0]))
		var d, s float64
		for i := 0; i < n; i++ {
			x, y := float64(a[0][i]), float64(b[0][i])
			d += (x - y) * (x - y)
			s += x * x
		}
		return 10 * math.Log10(d/s)
	}
	on := relDiffDb(render(loud, "on"), render(quiet, "on"))
	off := relDiffDb(render(loud, "off"), render(quiet, "off"))
	t.Logf("difference between loud and quiet source: auto-level on %.1f dB, off %.1f dB", on, off)
	if on > -35 {
		t.Errorf("with auto level the two renders should match, difference %.1f dB", on)
	}
	if off < on+10 { // 差が大きいほど値は大きい(0 dBに近い)
		t.Errorf("without auto level the renders should differ clearly more (on %.1f dB, off %.1f dB)", on, off)
	}
}
