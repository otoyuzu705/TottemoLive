package render

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"tottemolive/internal/project"
)

// trackedSource は、Read の最中に Close されていないか(先読みの Read とデコーダーの Close の競合)と、
// Close されたかを記録する合成音源。failAfter フレームを読んだ後の Read は errBoom を返す(0なら失敗しない)。
type trackedSource struct {
	*synthSource
	failAfter int
	delay     time.Duration
	inRead    atomic.Int32
	closed    atomic.Bool
	violation atomic.Bool
}

var errBoom = errors.New("boom")

func (s *trackedSource) Read(dst [][]float32) (int, error) {
	s.inRead.Add(1)
	defer s.inRead.Add(-1)
	if s.closed.Load() {
		s.violation.Store(true) // Close 後の Read
	}
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	if s.failAfter > 0 && s.pos >= s.failAfter {
		return 0, errBoom
	}
	return s.synthSource.Read(dst)
}

func (s *trackedSource) Close() error {
	if s.inRead.Load() != 0 {
		s.violation.Store(true) // Read の最中の Close
	}
	s.closed.Store(true)
	return nil
}

// trackedOpener は、開いた音源を srcs に記録する Engine.openSource。
func trackedOpener(srcs *[]*trackedSource, failAfter int, delay time.Duration) func(context.Context, string, int, func(float64)) (frameReader, error) {
	return func(ctx context.Context, path string, sr int, progress func(float64)) (frameReader, error) {
		s := &trackedSource{synthSource: newSynthSource(20*sr, 1), failAfter: failAfter, delay: delay}
		*srcs = append(*srcs, s)
		return s, nil
	}
}

// settleGoroutines は、ゴルーチン数が base 以下に戻るのを最大3秒待って、その数を返す。
func settleGoroutines(base int) int {
	n := runtime.NumGoroutine()
	for i := 0; i < 60 && n > base; i++ {
		time.Sleep(50 * time.Millisecond)
		n = runtime.NumGoroutine()
	}
	return n
}

func checkSources(t *testing.T, srcs []*trackedSource) {
	t.Helper()
	if len(srcs) == 0 {
		t.Fatal("no source opened")
	}
	for i, s := range srcs {
		if s.violation.Load() {
			t.Errorf("source %d: Read と Close が重なった(または Close 後に Read した)", i)
		}
		if !s.closed.Load() {
			t.Errorf("source %d: Close されていない", i)
		}
	}
}

// 途中でキャンセルしても、先読みのゴルーチンが残らず、Read の最中にデコーダーを閉じない。
func TestPrefetchCancelNoLeak(t *testing.T) {
	for _, cached := range []bool{false, true} {
		var srcs []*trackedSource
		e := &Engine{chunk: 8192}
		if cached {
			e = newTestEngine(t)
			e.chunk = 8192
		}
		e.openSource = trackedOpener(&srcs, 0, time.Millisecond)
		p := synthProject(20)
		p.PA.AutoLevel = "off"
		base := runtime.NumGoroutine()
		ctx, cancel := context.WithCancel(context.Background())
		prog := Progress(func(stage string, ratio float64) {
			if stage == StageProcess && ratio > 0.3 {
				cancel()
			}
		})
		_, err := e.RenderTo(ctx, p, prog, &discardSink{})
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cached=%v: err %v, want context.Canceled", cached, err)
		}
		checkSources(t, srcs)
		if n := settleGoroutines(base); n > base {
			t.Errorf("cached=%v: goroutines %d -> %d", cached, base, n)
		}
	}
}

// バスの Read が途中で失敗しても、エラーが返り、ゴルーチンは残らず、Read の最中に Close しない。
func TestPrefetchReadError(t *testing.T) {
	for _, cached := range []bool{false, true} {
		var srcs []*trackedSource
		e := &Engine{chunk: 8192}
		if cached {
			e = newTestEngine(t)
			e.chunk = 8192
		}
		e.openSource = trackedOpener(&srcs, 30000, 0)
		p := synthProject(20)
		p.PA.AutoLevel = "off"
		base := runtime.NumGoroutine()
		_, err := e.RenderTo(context.Background(), p, nil, &discardSink{})
		if !errors.Is(err, errBoom) {
			t.Fatalf("cached=%v: err %v, want boom", cached, err)
		}
		checkSources(t, srcs)
		if n := settleGoroutines(base); n > base {
			t.Errorf("cached=%v: goroutines %d -> %d", cached, base, n)
		}
	}
}

// 音源が開けない・存在しないときはエラーになり、ゴルーチンが残らない。
func TestPrefetchMissingSource(t *testing.T) {
	e := &Engine{openSource: synthOpener}
	p := synthProject(5)
	p.Sources[0].Path = "synth:notanumber"
	base := runtime.NumGoroutine()
	if _, err := e.RenderTo(context.Background(), p, nil, &discardSink{}); err == nil {
		t.Fatal("expected an error for an unreadable source")
	}
	if n := settleGoroutines(base); n > base {
		t.Errorf("goroutines %d -> %d", base, n)
	}
	// 実ファイル(存在しない)
	q := project.New()
	q.Sources = []project.Source{{ID: "x", Path: t.TempDir() + "/nonexistent.wav", Role: project.RoleMix}}
	q.Venue = project.DefaultVenue()
	q.Venue.Preset = "livehouse"
	if _, err := (&Engine{}).RenderTo(context.Background(), q, nil, &discardSink{}); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// 先読みしても、音源が複数でも、チャンクの大きさが違っても出力は変わらない(2面を使い回しても壊れない)。
func TestPrefetchChunkInvariance(t *testing.T) {
	p := synthProject(12)
	p.Sources = append(p.Sources, project.Source{ID: "t", Path: "synth:7", Role: project.RoleMix, GainDb: -3})
	var want [][]float32
	for _, chunk := range []int{1 << 20, 4096, 997} {
		e := &Engine{openSource: synthOpener, chunk: chunk}
		got, err := e.renderMem(context.Background(), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = got.Audio
			continue
		}
		if _, n := compareAudioQuiet(got.Audio, want); n != 0 {
			t.Errorf("chunk %d: %d samples differ", chunk, n)
		}
	}
}
