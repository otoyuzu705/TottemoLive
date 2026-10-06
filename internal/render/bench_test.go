package render

// 処理時間のベンチマーク(合成音源。ffmpeg もディスクの音源も使わない)。
// 時間がかかるので TOTTEMOLIVE_LONG_TESTS=1 のときだけ動く。
//
//	TOTTEMOLIVE_LONG_TESTS=1 go test ./internal/render -run '^$' -bench . -benchtime 1x -count 10 -cpu 4,16
//
// 結果は benchstat 風に比べられるよう、-count を重ねた行をそのまま出す(benchstat は入れていない)。

import (
	"context"
	"fmt"
	"testing"

	"tottemolive/internal/project"
	"tottemolive/internal/venue"
)

const benchSec = 4 * 60

var benchVenues = []string{"livehouse", "arena", "dome"}

func benchProject(venueID string, sec int) project.Project {
	p := synthProject(sec)
	if venueID != "livehouse" {
		p = applyVenueForTest(p, venueID)
	}
	return p
}

func benchEngine(b *testing.B, cache bool) *Engine {
	b.Helper()
	e := &Engine{openSource: synthOpener, baseDir: b.TempDir()}
	if cache {
		e = NewEngineWith(EngineConfig{CacheEnabled: true, Dir: b.TempDir()})
		e.openSource = synthOpener
		b.Cleanup(func() { e.Close() })
	}
	return e
}

// 書き出し(キャッシュなし)と、プレビュー初回(キャッシュ付きの新しいEngine)。4分、会場ごと。
func BenchmarkE2E(b *testing.B) {
	requireLongTests(b)
	for _, v := range benchVenues {
		p := benchProject(v, benchSec)
		b.Run(v+"/export", func(b *testing.B) {
			e := benchEngine(b, false)
			for i := 0; i < b.N; i++ {
				if _, err := e.RenderTo(context.Background(), p, nil, &discardSink{}); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(v+"/previewFirst", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				e := benchEngine(b, true)
				b.StartTimer()
				if _, err := e.PreviewTo(context.Background(), p, nil, &discardSink{}); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				e.Close()
				b.StartTimer()
			}
		})
	}
}

// キャッシュ済みのプレビューに値の変更を入れたときの再計算の時間。値は毎回変えて、必ずキーを変える。
func BenchmarkPreviewChange(b *testing.B) {
	requireLongTests(b)
	cases := []struct {
		name string
		mod  func(p *project.Project, i int)
	}{
		{"pa.lowCutHz", func(p *project.Project, i int) { p.PA.LowCutHz = 35 + float64(i%2)*5 + 1 }},
		{"reverb.decayScale", func(p *project.Project, i int) { p.Reverb.DecayScale = 0.9 + float64(i%2)*0.1 }},
		{"listener.x", func(p *project.Project, i int) { p.Listener.X = float64(i%2)*3 + 1 }},
		{"reverb.mix", func(p *project.Project, i int) { p.Reverb.Mix = 0.3 + float64(i%2)*0.1 }},
	}
	for _, v := range benchVenues {
		base := benchProject(v, benchSec)
		for _, tc := range cases {
			b.Run(v+"/"+tc.name, func(b *testing.B) {
				e := benchEngine(b, true)
				if _, err := e.PreviewTo(context.Background(), base, nil, &discardSink{}); err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					q := base.Clone()
					tc.mod(&q, i)
					if _, err := e.PreviewTo(context.Background(), q, nil, &discardSink{}); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// 先行プレビュー(窓)の応答時間。warmPA: PA段がキャッシュにある(曲全体のプレビュー後のシーク)、
// coldPA: 無い(最初の窓。PA段は曲全体を処理する)。窓は曲の中ほど。
func BenchmarkPreviewWindow(b *testing.B) {
	requireLongTests(b)
	for _, v := range benchVenues {
		p := benchProject(v, benchSec)
		b.Run(v+"/warmPA", func(b *testing.B) {
			e := benchEngine(b, true)
			if _, err := e.PreviewWindow(context.Background(), p, 100); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.PreviewWindow(context.Background(), p, 100+float64(i%3)); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(v+"/coldPA", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				e := benchEngine(b, true)
				b.StartTimer()
				if _, err := e.PreviewWindow(context.Background(), p, 100); err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				e.Close()
				b.StartTimer()
			}
		})
	}
}

// benchBusChunks は、4分ぶんのバス(ステレオ)を streamBlock ずつ渡すための元の1チャンクと、チャンク数。
func benchSourceChunk() [][]float32 {
	s := newSynthSource(streamBlock, 1)
	buf := [][]float32{make([]float32, streamBlock), make([]float32, streamBlock)}
	s.Read(buf)
	return buf
}

func benchChunks() int { return benchSec * sampleRate / streamBlock }

// 部品: paProc.Process(4分ぶん。ゲイン・Biquad・コンプ・歪み)。
func BenchmarkPAProc(b *testing.B) {
	requireLongTests(b)
	src := benchSourceChunk()
	buf := [][]float32{make([]float32, streamBlock), make([]float32, streamBlock)}
	p := project.New()
	for i := 0; i < b.N; i++ {
		pa := newPAProc(sampleRate, 6, p.PA) // 入力を大きめにして、コンプ・歪みを実際に動かす
		for k := 0; k < benchChunks(); k++ {
			copy(buf[0], src[0])
			copy(buf[1], src[1])
			pa.Process(buf)
		}
	}
}

// 部品: directProc(4分ぶん Push して Flush)・reverbProc(同)。
func BenchmarkDirectReverbProc(b *testing.B) {
	requireLongTests(b)
	src := benchSourceChunk()
	for _, v := range benchVenues {
		p := benchProject(v, benchSec)
		pp, err := prepare(p)
		if err != nil {
			b.Fatal(err)
		}
		ir := irFor(pp)
		b.Run(fmt.Sprintf("%s/directProc", v), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				d := newDirectProc(pp)
				for k := 0; k < benchChunks(); k++ {
					d.Push(src)
					d.out.discardBefore(d.out.produced)
				}
				d.Flush()
			}
		})
		b.Run(fmt.Sprintf("%s/reverbProc", v), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				r := newReverbProc(pp, ir)
				for k := 0; k < benchChunks(); k++ {
					r.Push(src)
					r.out.discardBefore(r.out.produced)
				}
				r.Flush()
			}
		})
	}
}

func irFor(pp *prepared) [][]float32 {
	return venue.BuildIR(pp.pr, pp.p.Reverb, sampleRate)
}
