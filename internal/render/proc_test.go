package render

import (
	"context"
	"math/rand"
	"testing"

	"tottemolive/internal/project"
	"tottemolive/internal/venue"
)

// 処理器(directProc / reverbProc / paProc / mixer)は、バスの区切り方に依らず、
// 全体を一度に処理する参照実装(legacy)とビット単位で同じ結果になる。

func procProject(preset string, subOn bool, speakers int) project.Project {
	p := project.New()
	p.Sources = []project.Source{{ID: "x", Path: "unused.wav", Role: project.RoleMix}}
	p.Venue = project.DefaultVenue()
	p.Venue.Preset = preset
	p.Venue = applyVenueForTest(p, preset).Venue
	if speakers == 1 {
		p.Venue.Speakers = p.Venue.Speakers[:1]
	}
	if subOn {
		p.Sub.Enabled = "on"
	} else {
		p.Sub.Enabled = "off"
	}
	p.Listener = project.Listener{X: 3, Y: 20, Z: 1.2}
	return p
}

func randBus(rng *rand.Rand, n int) [][]float32 {
	b := [][]float32{make([]float32, n), make([]float32, n)}
	for c := range b {
		for i := range b[c] {
			b[c][i] = (rng.Float32()*2 - 1) * 0.5
		}
	}
	return b
}

func sliceBus(bus [][]float32, from, to int) [][]float32 {
	return [][]float32{bus[0][from:to], bus[1][from:to]}
}

func TestDirectAndReverbProcChunkInvariance(t *testing.T) {
	ctx := context.Background()
	rng := rand.New(rand.NewSource(61))
	cases := []struct {
		preset string
		subOn  bool
		spk    int
	}{{"livehouse", true, 2}, {"livehouse", false, 2}, {"arena", true, 1}}
	for _, c := range cases {
		p := procProject(c.preset, c.subOn, c.spk)
		pp, err := prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		ir := venue.BuildIR(pp.pr, pp.p.Reverb, sampleRate)
		for _, n := range []int{1, 5000, 4096 * 3, 30001} {
			bus := randBus(rng, n)
			total := n + legacyMaxTailSamples(pp.pr)
			wantD, err := legacyDirectCompute(ctx, pp, bus, total)
			if err != nil {
				t.Fatal(err)
			}
			wantR, err := legacyReverbCompute(ctx, pp, bus, ir, total)
			if err != nil {
				t.Fatal(err)
			}
			for _, size := range []int{997, 4096, 65536} {
				d, r := newDirectProc(pp), newReverbProc(pp, ir)
				for from := 0; from < n; from += size {
					b := sliceBus(bus, from, min(from+size, n))
					d.Push(b)
					r.Push(b)
				}
				d.Flush()
				r.Flush()
				if d.out.produced > total || r.out.produced > total {
					t.Fatalf("%+v n=%d: produced %d / %d exceeds total %d", c, n, d.out.produced, r.out.produced, total)
				}
				if m, k := compareAudio(t, "direct", d.out.collect(total), wantD); m != 0 || k != 0 {
					t.Fatalf("%+v n=%d chunk=%d: direct differs", c, n, size)
				}
				if m, k := compareAudio(t, "reverb", r.out.collect(total), wantR); m != 0 || k != 0 {
					t.Fatalf("%+v n=%d chunk=%d: reverb differs", c, n, size)
				}
			}
			// 包み(directCompute / reverbCompute)も同じ
			gotD, _ := directCompute(ctx, pp, bus, total)
			gotR, _ := reverbCompute(ctx, pp, bus, ir, total)
			if m, k := compareAudio(t, "directCompute", gotD, wantD); m != 0 || k != 0 {
				t.Fatalf("directCompute differs")
			}
			if m, k := compareAudio(t, "reverbCompute", gotR, wantR); m != 0 || k != 0 {
				t.Fatalf("reverbCompute differs")
			}
		}
	}
}

func TestPAProcChunkInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(62))
	p := project.New()
	p.PA.CompRatio = 4
	p.PA.Drive = 0.4
	for _, n := range []int{1, 1000, 30001} {
		src := randBus(rng, n)
		want := [][]float32{append([]float32(nil), src[0]...), append([]float32(nil), src[1]...)}
		legacyApplyPA(want, sampleRate, -3.5, p.PA)
		for _, size := range []int{1, 7, 997, 65536} {
			if size == 1 && n > 1000 {
				continue
			}
			got := [][]float32{append([]float32(nil), src[0]...), append([]float32(nil), src[1]...)}
			proc := newPAProc(sampleRate, -3.5, p.PA)
			for from := 0; from < n; from += size {
				proc.Process(sliceBus(got, from, min(from+size, n)))
			}
			if m, k := compareAudio(t, "pa", got, want); m != 0 || k != 0 {
				t.Fatalf("n=%d chunk=%d: PA differs", n, size)
			}
		}
	}
}

// ミックスは、入力の到着が細切れでも(残響が遅れて届く・直接音のほうが長い・短いなど)、legacyMix と同じ。
func TestMixerMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(63))
	g := mixGains{direct: 0.7, reverb: 1.3}
	for _, c := range []struct{ nd, nr, delay, outLen int }{
		{5000, 7000, 300, 7000}, {5000, 3000, 0, 5000}, {100, 100, 5000, 100}, {70000, 90000, 1234, 90000},
	} {
		d, r := randBus(rng, c.nd), randBus(rng, c.nr)
		// legacy の入力は、出力の長さに揃えた直接音と、(outLen - delay) に切った残響
		direct := [][]float32{make([]float32, c.outLen), make([]float32, c.outLen)}
		for ch := range direct {
			copy(direct[ch], d[ch])
		}
		want := legacyMix(g, direct, legacyCrop(r, max(c.outLen-c.delay, 0)), c.delay)
		for _, size := range []int{1000, 65536, 1 << 20} {
			dq, rq := newFrameQueue(2), newFrameQueue(2)
			var got [][]float32 = [][]float32{nil, nil}
			m := &mixer{g: g, revDelay: c.delay, d: dq, r: rq, emit: func(b [][]float32) error {
				for ch := range got {
					got[ch] = append(got[ch], b[ch]...)
				}
				return nil
			}}
			for from := 0; from < max(c.nd, c.nr); from += size {
				if from < c.nd {
					dq.push(sliceBus(d, from, min(from+size, c.nd)))
				}
				if from < c.nr {
					rq.push(sliceBus(r, from, min(from+size, c.nr)))
				}
				if err := m.drain(c.outLen); err != nil {
					t.Fatal(err)
				}
			}
			dq.end()
			rq.end()
			if err := m.drain(c.outLen); err != nil {
				t.Fatal(err)
			}
			if m, k := compareAudio(t, "mix", got, want); m != 0 || k != 0 {
				t.Fatalf("%+v chunk=%d: mix differs", c, size)
			}
		}
		if m, k := compareAudio(t, "mix wrapper", mix(g, direct, legacyCrop(r, max(c.outLen-c.delay, 0)), c.delay), want); m != 0 || k != 0 {
			t.Fatalf("%+v: mix wrapper differs", c)
		}
	}
}

func TestFrameQueue(t *testing.T) {
	q := newFrameQueue(2)
	q.push([][]float32{{1, 2, 3}, {4, 5, 6}})
	q.push([][]float32{{7}, {8}})
	if q.availFrom(1) != 3 || q.availFrom(4) != 0 || q.availFrom(9) != 0 {
		t.Errorf("avail %d %d %d", q.availFrom(1), q.availFrom(4), q.availFrom(9))
	}
	dst := [][]float32{make([]float32, 5), make([]float32, 5)}
	q.discardBefore(2)
	q.readAt(2, dst) // 位置 2,3 のあとは、まだ無い所なので 0
	if dst[0][0] != 3 || dst[0][1] != 7 || dst[1][1] != 8 || dst[0][2] != 0 {
		t.Errorf("read %v", dst)
	}
	q.end()
	if q.availFrom(100) <= 1<<40 {
		t.Error("ended queue should be unbounded")
	}
}
