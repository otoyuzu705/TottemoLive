package venue

import (
	"math"
	"math/rand"
	"reflect"
	"testing"

	"gonum.org/v1/gonum/dsp/fourier"

	"tottemolive/internal/dsp"
	"tottemolive/internal/project"
)

func TestDefaultVenueMatchesArenaPreset(t *testing.T) {
	arena, _ := Get("arena")
	d := project.DefaultVenue()
	if d.Preset != "arena" || !reflect.DeepEqual(d.Speakers, arena.Speakers) || !reflect.DeepEqual(d.Subs, arena.Subs) {
		t.Errorf("project default venue differs from arena preset")
	}
	// 既定のリスナー(0,25)が部屋の中、既定の残響が表の既定値と一致
	p := project.New()
	if p.Reverb != arena.Reverb {
		t.Errorf("reverb default %+v vs arena %+v", p.Reverb, arena.Reverb)
	}
}

func TestApply(t *testing.T) {
	p := project.New()
	p.Listener.X, p.Listener.Y = 40, 60
	q, err := Apply(p, "livehouse")
	if err != nil {
		t.Fatal(err)
	}
	if q.Venue.Preset != "livehouse" || q.Venue.Speakers[1].X != 3 || q.Venue.Subs[1].X != 2 || q.Reverb.PreDelayMs != 8 {
		t.Errorf("not applied: %+v", q.Venue)
	}
	if q.Listener.X != 6 || q.Listener.Y != 14 {
		t.Errorf("listener not clamped: %+v", q.Listener)
	}
	if p.Venue.Preset != "arena" || p.Venue.Speakers[0].X != -12 {
		t.Error("input project was modified")
	}
	if _, err := Apply(p, "nope"); err == nil {
		t.Error("unknown preset accepted")
	}
	// プリセットの残響値は範囲内
	for _, pr := range List() {
		q := project.New()
		q.Reverb = pr.Reverb
		q.Normalize()
		if q.Reverb != pr.Reverb {
			t.Errorf("%s: reverb defaults out of range", pr.ID)
		}
	}
}

func TestBuildIR(t *testing.T) {
	const sr = 48000
	pr, _ := Get("hall")
	r := pr.Reverb
	ir := BuildIR(pr, r, sr)
	if len(ir) != 2 || len(ir[0]) != len(ir[1]) {
		t.Fatal("stereo IR expected")
	}
	// 中域のエネルギー密度が、白色IR(チャンネルあたりエネルギー1)と同じ
	if got, want := midBandEnergy(ir, sr)/2, normBandTarget(sr); math.Abs(10*math.Log10(got/want)) > 0.05 {
		t.Errorf("mid band energy %.4f, want %.4f", got, want)
	}
	e := 0.0
	for _, c := range ir {
		for _, v := range c {
			e += float64(v) * float64(v)
		}
	}
	// プリディレイぶんは無音
	pre := int(r.PreDelayMs * 1e-3 * sr)
	for i := 0; i < pre; i++ {
		if ir[0][i] != 0 {
			t.Fatal("pre-delay not silent")
		}
	}
	// decayScale を上げると長くなる
	r2 := r
	r2.DecayScale = 1.2
	if len(BuildIR(pr, r2, sr)[0]) <= len(ir[0]) {
		t.Error("decayScale should lengthen IR")
	}
	// 左右の相関: 低域は自然な拡散音場に合わせて相関が高い(全体の相関にも少し出る)が、中高域は無相関のまま
	dot := 0.0
	for i := range ir[0] {
		dot += float64(ir[0][i]) * float64(ir[1][i])
	}
	if math.Abs(dot/e) > 0.12 {
		t.Errorf("L/R correlated overall: %v", dot/e)
	}
	for _, fc := range []float64{2000, 4000} {
		if c := bandCorr(ir, fc); math.Abs(c) > 0.08 {
			t.Errorf("L/R correlated at %v Hz: %.2f", fc, c)
		}
	}
	// 決定的
	if !reflect.DeepEqual(ir, BuildIR(pr, r, sr)) {
		t.Error("IR is not deterministic")
	}
}

// bandpass は中心周波数 fc のオクターブ帯域(4次のバンドパス)を取り出す。
func bandpass(ir []float32, fc float64) []float32 {
	x := append([]float32(nil), ir...)
	dsp.LR4HighPass(x, 48000, fc/math.Sqrt2)
	dsp.LR4LowPass(x, 48000, fc*math.Sqrt2)
	return x
}

func bandEnergyDb(ir []float32, fc float64) float64 {
	e := 0.0
	for _, v := range bandpass(ir, fc) {
		e += float64(v) * float64(v)
	}
	return 10 * math.Log10(e)
}

// bandCorr は左右のIRの、帯域内での相関(0 = 無相関、1 = 同じ信号)。
func bandCorr(ir [][]float32, fc float64) float64 {
	l, r := bandpass(ir[0], fc), bandpass(ir[1], fc)
	var ll, rr, lr float64
	for i := range l {
		ll += float64(l[i]) * float64(l[i])
		rr += float64(r[i]) * float64(r[i])
		lr += float64(l[i]) * float64(r[i])
	}
	return lr / math.Sqrt(ll*rr)
}

// bandRT は帯域内の残響時間(シュレーダー積分のT30を2倍、秒)。
func bandRT(ir []float32, fc float64) float64 {
	x := bandpass(ir, fc)
	e := make([]float64, len(x)+1)
	for i := len(x) - 1; i >= 0; i-- {
		e[i] = e[i+1] + float64(x[i])*float64(x[i])
	}
	at := func(db float64) float64 {
		for i := range x {
			if 10*math.Log10(e[i]/e[0]) <= db {
				return float64(i) / 48000
			}
		}
		return math.NaN()
	}
	return 2 * (at(-35) - at(-5))
}

// 低域の左右の相関は lowCoherence で決まり、境界より上は無相関のまま。
// 低域の帯域は自由度が少なく(63 Hz帯で約100)、減衰もあるので、相関の推定は ±0.2 ほどばらつく。
func TestBuildIRLowCoherence(t *testing.T) {
	pr, _ := Get("arena")
	for _, c := range []float64{0, 0.5, 1} {
		r := pr.Reverb
		r.LowCoherence = c
		ir := BuildIR(pr, r, 48000)
		for _, fc := range []float64{63, 125} {
			if got := bandCorr(ir, fc); math.Abs(got-c) > 0.2 {
				t.Errorf("lowCoherence=%v: %v Hz correlation %.2f", c, fc, got)
			}
		}
		for _, fc := range []float64{1000, 4000} {
			if got := bandCorr(ir, fc); math.Abs(got) > 0.08 {
				t.Errorf("lowCoherence=%v: %v Hz should stay decorrelated, got %.2f", c, fc, got)
			}
		}
	}
}

// 低域の残響時間は lowDecayScale 倍になり、中高域の残響時間は変わらない。
func TestBuildIRLowDecay(t *testing.T) {
	pr, _ := Get("hall")
	r := pr.Reverb
	r.LowDecayScale = 1.6
	ir := BuildIR(pr, r, 48000)
	main, low := bandRT(ir[0], 1000), bandRT(ir[0], 63)
	if math.Abs(main-pr.RT60Sec)/pr.RT60Sec > 0.1 {
		t.Errorf("mid RT %.2f, want ~%.2f", main, pr.RT60Sec)
	}
	if got := low / main; math.Abs(got-1.6) > 0.2 {
		t.Errorf("low/mid RT ratio %.2f, want ~1.6", got)
	}
	if got := len(ir[0]); got != int((pr.RT60Sec*1.6*irTailMargin+r.PreDelayMs*1e-3)*48000) {
		t.Errorf("IR length %d does not follow the longest decay", got)
	}
	if math.Abs(IRSeconds(pr, r)*48000-float64(len(ir[0]))) > 1 {
		t.Errorf("IRSeconds disagrees with BuildIR length")
	}
}

// 低域の残響レベルは、その分だけ低域を増減し、中高域はほとんど変えない。エネルギーは常に1にそろう。
func TestBuildIRLowLevel(t *testing.T) {
	pr, _ := Get("arena")
	base := pr.Reverb
	base.LowLevelDb = 0
	boost := base
	boost.LowLevelDb = 6
	a, b := BuildIR(pr, base, 48000), BuildIR(pr, boost, 48000)
	if d := bandEnergyDb(b[0], 63) - bandEnergyDb(a[0], 63); math.Abs(d-6) > 1 {
		t.Errorf("+6 dB raised the 63 Hz band by %.1f dB", d)
	}
	if d := bandEnergyDb(b[0], 2000) - bandEnergyDb(a[0], 2000); math.Abs(d) > 0.5 {
		t.Errorf("mid band moved by %.1f dB", d)
	}
	// 低域のレベルを動かしても、中域の基準は動かない(中域でそろえるため)
	for _, ir := range [][][]float32{a, b} {
		if got, want := midBandEnergy(ir, 48000)/2, normBandTarget(48000); math.Abs(10*math.Log10(got/want)) > 0.05 {
			t.Errorf("mid band energy %.4f, want %.4f", got, want)
		}
	}
}

// MaxDistanceM は、会場内の最も遠いスピーカー(横に開いた位置)と客席の隅の距離を覆う。
func TestMaxDistanceCoversTheRoom(t *testing.T) {
	for _, pr := range List() {
		max := MaxDistanceM(pr)
		for _, sp := range pr.Speakers {
			for _, x := range []float64{-pr.WidthM / 2, pr.WidthM / 2} {
				d := math.Sqrt((x-sp.X)*(x-sp.X) + pr.DepthM*pr.DepthM + sp.Z*sp.Z)
				if d > max {
					t.Errorf("%s: distance %.1f m exceeds MaxDistanceM %.1f m", pr.ID, d, max)
				}
			}
		}
	}
}

// 高域ダンプ・残響の長さ・低域の長さを変えても、残響の中域のレベルは変わらない。ダンプは高域だけを変える。
func TestBuildIRNormalizesMidBand(t *testing.T) {
	pr, _ := Get("arena")
	base := BuildIR(pr, pr.Reverb, 48000)
	mid0 := midBandEnergy(base, 48000)
	hi0 := bandEnergyDb(base[0], 8000)
	for name, mod := range map[string]func(*project.Reverb){
		"highDamp 2000":  func(r *project.Reverb) { r.HighDampHz = 2000 },
		"highDamp 16000": func(r *project.Reverb) { r.HighDampHz = 16000 },
		"decay 0.5":      func(r *project.Reverb) { r.DecayScale = 0.5 },
		"low decay 2.5":  func(r *project.Reverb) { r.LowDecayScale = 2.5 },
		"low level +12":  func(r *project.Reverb) { r.LowLevelDb = 12 },
	} {
		r := pr.Reverb
		mod(&r)
		ir := BuildIR(pr, r, 48000)
		if d := 10 * math.Log10(midBandEnergy(ir, 48000)/mid0); math.Abs(d) > 0.05 {
			t.Errorf("%s: mid band moved by %.2f dB", name, d)
		}
	}
	// ダンプを強めると、高域だけが下がる(中域が持ち上がらない)
	r := pr.Reverb
	r.HighDampHz = 2000
	if d := bandEnergyDb(BuildIR(pr, r, 48000)[0], 8000) - hi0; d > -3 {
		t.Errorf("8 kHz band should drop with a strong damp, moved %.1f dB", d)
	}
}

// 臨界距離: 大きく残響の長い会場ほど遠く、残響を長くすると短くなる。典型的な値の範囲に収まる。
func TestCriticalDistance(t *testing.T) {
	want := map[string][2]float64{ // 会場: 臨界距離の妥当な範囲(m)
		"club": {3, 8}, "livehouse": {5, 11}, "hall": {10, 25}, "arena": {30, 55}, "dome": {55, 100}, "outdoor": {100, 300},
	}
	var last float64
	for _, id := range []string{"club", "livehouse", "hall", "arena", "dome"} {
		pr, _ := Get(id)
		dc := CriticalDistanceM(pr, 1)
		if r := want[id]; dc < r[0] || dc > r[1] {
			t.Errorf("%s: critical distance %.1f m, expected within %v", id, dc, r)
		}
		if dc <= last {
			t.Errorf("%s: critical distance %.1f m should grow with the venue size", id, dc)
		}
		last = dc
		if CriticalDistanceM(pr, 1.2) >= dc || CriticalDistanceM(pr, 0.5) <= dc {
			t.Errorf("%s: longer decay must shorten the critical distance", id)
		}
	}
	out, _ := Get("outdoor")
	arena, _ := Get("arena")
	if CriticalDistanceM(out, 1) <= CriticalDistanceM(arena, 1)*2 {
		t.Error("an open-air venue should have a much larger critical distance")
	}
	// 会場プリセットの残響量の既定値はすべて基準値(会場の違いは臨界距離で表す)
	for _, pr := range List() {
		if pr.Reverb.Mix != NominalMix {
			t.Errorf("%s: mix %v, want the nominal %v", pr.ID, pr.Reverb.Mix, NominalMix)
		}
	}
}

// 高域は中域より早く減衰する: 境界(highDecayHz)の2倍より上の残響時間は中域の highDecayScale 倍、
// 境界より下の中域・低域の残響時間は変わらず、残響時間は周波数とともに短くなる(増えない)。
// オクターブ帯域の測定では、境界から2倍までは隣の節点が混ざった値になる。そのため、境界の2倍より上で目標との一致を確かめる。
func TestBuildIRHighDecay(t *testing.T) {
	pr, _ := Get("hall")
	for _, scale := range []float64{1, 0.6, 0.3} {
		r := pr.Reverb
		r.HighDecayScale = scale
		r.LowDecayScale = 1  // 低域は中域と同じ長さにして、高域だけを見る
		r.HighDampHz = 16000 // 静的な高域ダンプは、残響時間の測定の邪魔にならないよう最大にする
		ir := BuildIR(pr, r, 48000)
		mid := bandRT(ir[0], 1000)
		if math.Abs(mid-pr.RT60Sec)/pr.RT60Sec > 0.1 {
			t.Errorf("scale %v: mid RT %.2f, want ~%.2f", scale, mid, pr.RT60Sec)
		}
		for _, fc := range []float64{12000, 16000} { // 境界(4 kHz)の2倍より上
			if got := bandRT(ir[0], fc) / mid; math.Abs(got-scale) > 0.2*scale+0.05 {
				t.Errorf("scale %v: %v Hz RT / mid RT = %.2f", scale, fc, got)
			}
		}
		for _, fc := range []float64{125, 500, 2000} { // 境界より下は変わらない
			if got := bandRT(ir[0], fc) / mid; math.Abs(got-1) > 0.12 {
				t.Errorf("scale %v: %v Hz RT ratio %.2f should stay 1", scale, fc, got)
			}
		}
		prev := 2.0
		for _, fc := range []float64{2000, 4000, 6000, 8000, 12000, 16000} {
			got := bandRT(ir[0], fc) / mid
			if got > prev+0.08 {
				t.Errorf("scale %v: RT must not grow with frequency (%v Hz: %.2f after %.2f)", scale, fc, got, prev)
			}
			prev = got
		}
	}
}

// 高域の減衰を変えても、残響の中域のレベルは変わらない(中域で正規化)。高域のエネルギーは長さに応じて変わる(短いほど小さい)。
// 境界周波数を上げると、その分だけ高域が短くなり始める周波数が上がる。
func TestBuildIRHighDecayKeepsMidAndRespectsBoundary(t *testing.T) {
	pr, _ := Get("arena")
	base := pr.Reverb
	base.HighDecayScale = 1
	short := base
	short.HighDecayScale = 0.4
	a, b := BuildIR(pr, base, 48000), BuildIR(pr, short, 48000)
	if d := 10 * math.Log10(midBandEnergy(b, 48000)/midBandEnergy(a, 48000)); math.Abs(d) > 0.05 {
		t.Errorf("mid band moved by %.2f dB", d)
	}
	if d := bandEnergyDb(b[0], 10000) - bandEnergyDb(a[0], 10000); d > -2 {
		t.Errorf("shorter high decay should lower the 10 kHz energy, moved %.1f dB", d)
	}
	// 境界が2 kHzなら 5 kHz帯は(境界の2倍=4 kHzより上なので)短く、境界が8 kHzなら 5 kHz帯は中域のまま
	hi, lo := short, short
	hi.HighDecayHz, lo.HighDecayHz = 8000, 2000
	rtHi, rtLo := bandRT(BuildIR(pr, hi, 48000)[0], 5000), bandRT(BuildIR(pr, lo, 48000)[0], 5000)
	if rtHi <= rtLo*1.3 {
		t.Errorf("5 kHz band: RT %.2f s with boundary 8 kHz vs %.2f s with boundary 2 kHz (should be clearly longer)", rtHi, rtLo)
	}
	// 1を超える指定は1に丸める(IRの長さは変わらない)
	over := base
	over.HighDecayScale = 3
	if len(BuildIR(pr, over, 48000)[0]) != len(a[0]) {
		t.Error("a scale above 1 must not lengthen the IR")
	}
}

// thirdOctDensity は、IR(左右)の 1/3 オクターブ帯域(中心 fcs、fc·2^(±1/6))のパワー密度(dB、絶対値は意味を持たない)。
// IR全体を2のべき乗にゼロ詰めしてFFTし、左右のパワーを足して、帯域内のbinで平均する。
func thirdOctDensity(ir [][]float32, fs float64, fcs []float64) []float64 {
	N := irFFTLen(len(ir[0]))
	fft := fourier.NewFFT(N)
	pw := make([]float64, N/2+1)
	for _, ch := range ir {
		buf := make([]float64, N)
		for i, v := range ch {
			buf[i] = float64(v)
		}
		for b, c := range fft.Coefficients(nil, buf) {
			pw[b] += real(c)*real(c) + imag(c)*imag(c)
		}
	}
	out := make([]float64, len(fcs))
	for i, fc := range fcs {
		lo, hi := fc*math.Pow(2, -1.0/6), fc*math.Pow(2, 1.0/6)
		sum, n := 0.0, 0
		for b := int(math.Ceil(lo * float64(N) / fs)); b <= int(hi*float64(N)/fs) && b < len(pw); b++ {
			sum += pw[b]
			n++
		}
		out[i] = 10 * math.Log10(sum/float64(n))
	}
	return out
}

func thirdOctCenters(k0, k1 int) []float64 {
	var fcs []float64
	for k := k0; k <= k1; k++ {
		fcs = append(fcs, 1000*math.Pow(2, float64(k)/3))
	}
	return fcs
}

func TestHighDecayWeightsSumToOne(t *testing.T) {
	for _, hz := range []float64{2000, 4000, 12000} {
		for f := 0.0; f <= 24000; f += 7.3 {
			sum := 0.0
			u := highDecayPos(f, hz)
			for k := 0; k <= highDecayStages; k++ {
				sum += nodeWeight(u, k)
			}
			if math.Abs(sum-1) > 1e-12 {
				t.Fatalf("hz %v f %v: weights sum to %v", hz, f, sum)
			}
			if f <= hz && nodeWeight(u, 0) != 1 {
				t.Fatalf("hz %v f %v: node 0 weight %v, want 1", hz, f, nodeWeight(u, 0))
			}
			if f >= 2*hz && nodeWeight(u, highDecayStages) != 1 {
				t.Fatalf("hz %v f %v: last node weight %v, want 1", hz, f, nodeWeight(u, highDecayStages))
			}
		}
	}
}

func TestSplitByNodesSumsToInput(t *testing.T) {
	const N, n = 1 << 14, 10000
	rng := rand.New(rand.NewSource(1))
	x := make([]float32, N)
	for i := range x {
		x[i] = float32(rng.Float64()*2 - 1)
	}
	pos := make([]float64, N/2+1)
	for b := range pos {
		pos[b] = highDecayPos(float64(b)*48000/N, 4000)
	}
	parts := splitByNodes(x, pos, n)
	if len(parts) != highDecayStages+1 {
		t.Fatalf("%d components, want %d", len(parts), highDecayStages+1)
	}
	worst := 0.0
	for i := 0; i < n; i++ {
		sum := 0.0
		for _, p := range parts {
			sum += float64(p[i])
		}
		worst = math.Max(worst, math.Abs(sum-float64(x[i])))
	}
	if worst > 1e-6 {
		t.Errorf("components differ from the input by %g", worst)
	}
}

// 高域の減衰を無効(scale 1・ダンプ最大)にしたIRは、2〜10 kHz が平坦になる(帯域分割の打ち消しで穴が空かない)。
func TestBuildIRFlatHighBand(t *testing.T) {
	for _, pr := range List() {
		r := pr.Reverb
		r.HighDecayScale, r.HighDampHz = 1, 16000
		d := thirdOctDensity(BuildIR(pr, r, 48000), 48000, thirdOctCenters(0, 10))
		ref := (d[0] + d[1] + d[2] + d[3]) / 4
		for k := 3; k <= 10; k++ {
			if dev := d[k] - ref; math.Abs(dev) > 1.2 {
				t.Errorf("%s: %.0f Hz band is %+.1f dB from the 1-2 kHz level", pr.ID, 1000*math.Pow(2, float64(k)/3), dev)
			}
		}
	}
}

// 既定の残響設定では、4 kHz より上の 1/3 オクターブは隣の低い帯域より上がらない(穴の後で戻らない)。
func TestBuildIRHighBandDecreases(t *testing.T) {
	for _, pr := range List() {
		d := thirdOctDensity(BuildIR(pr, pr.Reverb, 48000), 48000, thirdOctCenters(5, 12))
		for i := 1; i < len(d); i++ {
			if d[i]-d[i-1] > 1 {
				t.Errorf("%s: band %d rises %.1f dB over the band below", pr.ID, i+5, d[i]-d[i-1])
			}
		}
	}
}
