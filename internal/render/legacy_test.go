package render

// 参照実装。チャンク処理(ストリーミング化)の前の run(キャッシュなし)と、そこから呼ぶ段の処理を、
// 一字一句そのまま残したもの(関数名に legacy を付けた以外は元のコード。呼び出し先の dsp / spatial の公開関数は
// そのまま呼ぶ。それらの同一性は各パッケージの ref テストで担保している)。
// 流し処理版の Render は、これとビット単位で一致することをテストで確かめる。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sync"
	"testing"

	"tottemolive/internal/audio"
	"tottemolive/internal/dsp"
	"tottemolive/internal/params"
	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
	"tottemolive/internal/venue"
)

// bitExactArch は、旧実装の写しと完全に同じ(ビット一致の)結果を要求できる環境か。
// arm64 は x*y+z を FMA(丸めが1回)に融合するコンパイラで、式が同じでも、融合されるかどうかが
// コードの置き場所で変わり、1サンプルあたり1 ULP(約6e-8)ずれることがある。amd64(CI・Windows)ではビット一致を要求し、
// それ以外では丸め誤差の範囲(1e-6、-120 dBFS。LUFSは1e-9)を許す。
func bitExactArch() bool { return runtime.GOARCH == "amd64" }

// sameAsLegacy は、旧実装との差(最大の絶対値 maxDiff、不一致のサンプル数 n)が許容の範囲内か。
func sameAsLegacy(maxDiff float64, n int) bool {
	if bitExactArch() {
		return n == 0 && maxDiff == 0
	}
	return maxDiff <= 1e-6
}

// legacyRender は現行の run をキャッシュなし・進捗なしで実行した結果(音声と出力の統合ラウドネス)を返す。
func legacyRender(ctx context.Context, p project.Project) ([][]float32, float64, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, 0, err
	}
	p = pp.p

	// 1. 音源
	bufs := make([][][]float32, len(p.Sources))
	for i, s := range p.Sources {
		bufs[i], err = audio.Decode(ctx, s.Path, sampleRate, nil)
		if err != nil {
			return nil, 0, err
		}
	}

	// 2. 処理
	alignDb := 0.0
	if p.PA.AutoLevel == "on" {
		sum := make([][]float32, 2)
		n := 0
		for _, b := range bufs {
			n = max(n, len(b[0]))
		}
		for c := range sum {
			sum[c] = make([]float32, n)
			for i, b := range bufs {
				g := float32(dsp.DbToLin(p.Sources[i].GainDb))
				for k, v := range b[c] {
					sum[c][k] += v * g
				}
			}
		}
		lufs := dsp.IntegratedLUFS(sum, sampleRate)
		if !math.IsInf(lufs, 0) && !math.IsNaN(lufs) {
			alignDb = math.Min(math.Max(p.PA.InputLufs-lufs, -maxAlignDb), maxAlignDb)
		}
	}
	for i := range bufs {
		legacyApplyPA(bufs[i], sampleRate, p.Sources[i].GainDb+alignDb, p.PA)
	}
	bus := legacySumBus(bufs)
	songLen := len(bus[0])

	ir := venue.BuildIR(pp.pr, p.Reverb, sampleRate)
	total := songLen + legacyMaxTailSamples(pp.pr)
	revDelay := firstArrivalSamples(p)
	outLen := songLen + revDelay + len(ir[0])

	var direct, reverb [][]float32
	var derr, rerr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		direct, derr = legacyDirectCompute(ctx, pp, bus, total)
	}()
	go func() {
		defer wg.Done()
		reverb, rerr = legacyReverbCompute(ctx, pp, bus, ir, total)
	}()
	wg.Wait()
	if err := errors.Join(derr, rerr); err != nil {
		return nil, 0, err
	}

	out := legacyMix(mixGainsFor(p, pp.pr), legacyCrop(direct, outLen), legacyCrop(reverb, outLen-revDelay), revDelay)
	legacyMaster(out, sampleRate, p.Output)
	return out, dsp.IntegratedLUFS(out, sampleRate), nil
}

func legacyCrop(buf [][]float32, n int) [][]float32 {
	out := make([][]float32, len(buf))
	for c, ch := range buf {
		out[c] = ch[:min(n, len(ch))]
	}
	return out
}

func legacyMaxTailSamples(pr venue.Preset) int {
	pre, _ := params.Find("reverb.preDelayMs")
	decay, _ := params.Find("reverb.decayScale")
	low, _ := params.Find("reverb.lowDecayScale")
	sec := venue.IRSeconds(pr, project.Reverb{PreDelayMs: pre.Max, DecayScale: decay.Max, LowDecayScale: low.Max})
	// 直接音と残響の伝搬遅延(最大の距離ぶん)も見込む
	sec += venue.MaxDistanceM(pr) / spatial.SpeedOfSound
	return int(math.Ceil(sec * sampleRate))
}

func legacySumBus(bufs [][][]float32) [][]float32 {
	n := 0
	for _, s := range bufs {
		n = max(n, len(s[0]))
	}
	bus := [][]float32{make([]float32, n), make([]float32, n)}
	for _, s := range bufs {
		for c := range bus {
			for i, v := range s[c] {
				bus[c][i] += v
			}
		}
	}
	return bus
}

func legacyApplyPA(buf [][]float32, sr int, gainDb float64, pa project.PA) {
	g := float32(dsp.DbToLin(gainDb))
	for _, ch := range buf {
		for i := range ch {
			ch[i] *= g
		}
		dsp.HighPass(float64(sr), pa.LowCutHz).Process(ch)
		dsp.LowShelf(float64(sr), pa.LowShelfHz, pa.LowShelfDb).Process(ch)
		dsp.HighShelf(float64(sr), pa.HighShelfHz, pa.HighShelfDb).Process(ch)
	}
	dsp.Compress(buf, sr, dsp.CompParams{
		ThresholdDb: pa.CompThresholdDb, Ratio: pa.CompRatio,
		AttackMs: pa.CompAttackMs, ReleaseMs: pa.CompReleaseMs,
	})
	dsp.Saturate(buf, pa.Drive)
}

func legacyDirectCompute(ctx context.Context, pp *prepared, in [][]float32, total int) ([][]float32, error) {
	p := pp.p
	spk := p.Venue.Speakers
	subOn := subsActive(p)
	out := [][]float32{make([]float32, total), make([]float32, total)}
	// スピーカーごとに並列に計算し、できた順ではなくスピーカーの順に足し込む。
	// 全スピーカーぶんの結果を同時に持たず、足し算の順序も固定になる
	turn := make([]chan struct{}, len(spk))
	for i := range turn {
		turn[i] = make(chan struct{})
	}
	errs := make([]error, len(spk))
	var wg sync.WaitGroup
	for i, s := range spk {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer close(turn[i])
			feed := legacySpeakerFeed(in, i, len(spk))
			if subOn {
				// 低域はサブが受け持つので、メインは中高域だけにする(元のバスは他の経路が使うので複製して掛ける)
				feed = append([]float32(nil), feed...)
				dsp.LR4HighPass(feed, sampleRate, p.Sub.CrossoverHz)
			}
			az, el, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
			r, err := spatial.Direct(ctx, feed, sampleRate, d, az, el, pp.set, spatial.DirectParams{
				Rolloff: p.Spatial.DistanceRolloff, AirAbsorption: p.Spatial.AirAbsorption,
			})
			if i > 0 {
				<-turn[i-1]
			}
			if err != nil {
				errs[i] = err
				return
			}
			for c := range out {
				legacyAddInto(out[c], r[c])
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if subOn {
		legacyAddSubs(out, in, p, pp.pr)
	}
	return out, nil
}

func legacyAddSubs(out, bus [][]float32, p project.Project, pr venue.Preset) {
	mono := make([]float32, len(bus[0]))
	for i := range mono {
		mono[i] = bus[0][i] + bus[1][i]
	}
	dsp.LR4LowPass(mono, sampleRate, p.Sub.CrossoverHz)
	g := float32(dsp.DbToLin(p.Sub.LevelDb) / float64(len(p.Venue.Subs)))
	for i := range mono {
		mono[i] *= g
	}
	extra := subAlignDelays(p, pr)
	for i, s := range p.Venue.Subs {
		_, _, d := spatial.Direction(p.Listener.X, p.Listener.Y, p.Listener.Z, p.Listener.YawDeg, s.X, s.Y, s.Z)
		sig := spatial.Sub(mono, sampleRate, d, extra[i], p.Spatial.DistanceRolloff)
		for c := range out {
			legacyAddInto(out[c], sig)
		}
	}
}

func legacyRadiatedMono(bus [][]float32, active bool, sub project.Sub) []float32 {
	n := len(bus[0])
	mono := make([]float32, n)
	if !active {
		for i := range mono {
			mono[i] = (bus[0][i] + bus[1][i]) / 2
		}
		return mono
	}
	fs := float64(sampleRate)
	tmp := make([]float32, n)
	for c := 0; c < 2; c++ {
		copy(tmp, bus[c])
		dsp.LR4HighPass(tmp, fs, sub.CrossoverHz)
		for i, v := range tmp {
			mono[i] += v / 2
		}
	}
	for i := range tmp {
		tmp[i] = bus[0][i] + bus[1][i]
	}
	dsp.LR4LowPass(tmp, fs, sub.CrossoverHz)
	g := float32(dsp.DbToLin(sub.LevelDb) / 2)
	for i, v := range tmp {
		mono[i] += v * g
	}
	return mono
}

func legacySpeakerFeed(bus [][]float32, i, count int) []float32 {
	if count == 1 {
		m := make([]float32, len(bus[0]))
		for k := range m {
			m[k] = (bus[0][k] + bus[1][k]) / 2
		}
		return m
	}
	return bus[i%2]
}

func legacyReverbCompute(ctx context.Context, pp *prepared, in, ir [][]float32, total int) ([][]float32, error) {
	sub := pp.p.Sub
	active := subsActive(pp.p)
	if !active {
		sub = project.Sub{}
	}
	mono := legacyRadiatedMono(in, active, sub)
	out := [][]float32{make([]float32, total), make([]float32, total)}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for c := 0; c < 2; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var y []float32
			y, errs[c] = dsp.Convolve(ctx, mono, ir[c])
			legacyAddInto(out[c], y)
		}()
	}
	wg.Wait()
	return out, errors.Join(errs...)
}

func legacyMix(g mixGains, direct, reverb [][]float32, reverbDelay int) [][]float32 {
	out := make([][]float32, 2)
	for c := range out {
		out[c] = make([]float32, len(direct[c]))
		for i := range out[c] {
			out[c][i] = direct[c][i] * g.direct
		}
		// 残響は、リスナーに最初の音が届く時刻(reverbDelay)から始まる
		for i, v := range reverb[c] {
			if j := i + reverbDelay; j < len(out[c]) {
				out[c][j] += v * g.reverb
			}
		}
	}
	return out
}

func legacyMaster(buf [][]float32, sr int, o project.Output) {
	lufs := dsp.IntegratedLUFS(buf, sr)
	if !math.IsInf(lufs, 0) && !math.IsNaN(lufs) {
		g := float32(dsp.DbToLin(o.TargetLufs - lufs))
		for _, ch := range buf {
			for i := range ch {
				ch[i] *= g
			}
		}
	}
	dsp.TruePeakLimit(buf, sr, o.CeilingDbTp)
}

func legacyAddInto(dst, src []float32) {
	n := min(len(dst), len(src))
	for i := 0; i < n; i++ {
		dst[i] += src[i]
	}
}

// compareAudio は2つの出力を比べ、最大の差・一致しないサンプル数を返す(t.Logf で報告する)。
func compareAudio(t *testing.T, name string, got, want [][]float32) (maxDiff float64, mismatches int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: channels %d vs %d", name, len(got), len(want))
	}
	for c := range want {
		if len(got[c]) != len(want[c]) {
			t.Fatalf("%s: ch%d length %d vs %d", name, c, len(got[c]), len(want[c]))
		}
		for i := range want[c] {
			if got[c][i] != want[c][i] {
				mismatches++
				maxDiff = math.Max(maxDiff, math.Abs(float64(got[c][i])-float64(want[c][i])))
			}
		}
	}
	t.Logf("%s: max|diff|=%.3g, mismatching samples=%d", name, maxDiff, mismatches)
	return maxDiff, mismatches
}

// 現行(参照)の全体処理と、Render の出力が、チャンクの大きさに依らず一致する。
func TestLegacyMatchesRender(t *testing.T) {
	base := twoSourceProject(t)
	cases := []struct {
		name string
		mod  func(*project.Project)
	}{
		{"livehouse", func(p *project.Project) {}},
		{"arena sub on", func(p *project.Project) {
			*p = applyVenueForTest(*p, "arena")
		}},
		{"livehouse sub off autolevel off", func(p *project.Project) {
			p.Sub.Enabled = "off"
			p.PA.AutoLevel = "off"
		}},
		{"one source", func(p *project.Project) {
			p.Sources = p.Sources[:1]
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base.Clone()
			tc.mod(&p)
			want, wantLufs, err := legacyRender(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			for _, chunk := range []int{997, 4096, 65536, 1 << 20} {
				got, err := (&Engine{chunk: chunk}).renderMem(context.Background(), p, nil)
				if err != nil {
					t.Fatal(err)
				}
				name := fmt.Sprintf("%s chunk=%d", tc.name, chunk)
				maxDiff, n := compareAudio(t, name, got.Audio, want)
				// 出力は旧実装とビット単位で一致する(1サンプルでも違えば失敗)
				if !sameAsLegacy(maxDiff, n) {
					t.Errorf("%s: max|diff| %g (%d samples differ)", name, maxDiff, n)
				}
				if d := math.Abs(got.LUFS - wantLufs); (bitExactArch() && got.LUFS != wantLufs) || d > 1e-9 {
					t.Errorf("%s: LUFS %v vs %v", name, got.LUFS, wantLufs)
				}
				if got.Frames != len(want[0]) {
					t.Errorf("%s: frames %d vs %d", name, got.Frames, len(want[0]))
				}
			}
		})
	}
}

func applyVenueForTest(p project.Project, id string) project.Project {
	q, err := venue.Apply(p, id)
	if err != nil {
		panic(err)
	}
	return q
}

// legacyOriginal は、流し処理にする前の Original(全音源をデコードして足し、マスターを通す)。
func legacyOriginal(ctx context.Context, p project.Project) ([][]float32, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	var out [][]float32
	for i, s := range pp.p.Sources {
		buf, err := audio.Decode(ctx, s.Path, sampleRate, nil)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = [][]float32{make([]float32, len(buf[0])), make([]float32, len(buf[0]))}
		}
		for c := range out {
			if len(buf[c]) > len(out[c]) {
				out[c] = append(out[c], make([]float32, len(buf[c])-len(out[c]))...)
			}
		}
		g := float32(math.Pow(10, pp.p.Sources[i].GainDb/20))
		for c := range out {
			for k, v := range buf[c] {
				out[c][k] += v * g
			}
		}
	}
	legacyMaster(out, sampleRate, pp.p.Output)
	return out, nil
}

func TestOriginalMatchesLegacy(t *testing.T) {
	p := twoSourceProject(t)
	want, err := legacyOriginal(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{997, 65536} {
		got, err := (&Engine{chunk: chunk}).Original(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if m, n := compareAudio(t, "original", got.Audio, want); !sameAsLegacy(m, n) {
			t.Fatalf("chunk %d: original differs", chunk)
		}
	}
}
