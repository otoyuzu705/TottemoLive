package render

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"tottemolive/internal/audio"
)

// 長い曲のメモリ測定。時間がかかるので TOTTEMOLIVE_LONG_TESTS=1 のときだけ動く。
func requireLongTests(t testing.TB) {
	t.Helper()
	if os.Getenv("TOTTEMOLIVE_LONG_TESTS") == "" {
		t.Skip("TOTTEMOLIVE_LONG_TESTS=1 のときだけ実行する")
	}
}

// peakHeap は f の実行中の HeapInuse の最大値(バイト)を10msごとの標本で記録する。
func peakHeap(f func()) (peak uint64) {
	runtime.GC()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var ms runtime.MemStats
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			runtime.ReadMemStats(&ms)
			peak = max(peak, ms.HeapInuse)
			select {
			case <-stop:
				return
			case <-tick.C:
			}
		}
	}()
	f()
	close(stop)
	wg.Wait()
	return peak
}

// makeLongSource は ffmpeg で指定した長さ(分)のステレオ音源(ノイズ + トーン)を作る。
func makeLongSource(t testing.TB, minutes int) string {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(t.TempDir(), "long"+strconv.Itoa(minutes)+".wav")
	d := strconv.Itoa(minutes * 60)
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "anoisesrc=d="+d+":c=pink:r=48000:a=0.2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+d+":sample_rate=48000",
		"-filter_complex", "[1]volume=0.3,tremolo=f=0.5:d=0.8[b];[0][b]amix=inputs=2:normalize=0,pan=stereo|c0=c0|c1=0.7*c0",
		"-c:a", "pcm_s16le", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

// TestMemoryPeakBaseline は、ffmpeg で作った実ファイルの曲(2分・10分)に対するヒープのピークを測る(書き出し経路 Render と
// プレビュー経路 Preview)。判定はせず、数字を記録する。Render / Preview は出力の音声をメモリに集めるので(memSink)、
// 出力ぶん(10分で約230MB)は含まれる。出力をファイルへ流す場合は TestMemoryStaysFlat のとおりほぼ一定になる。
func TestMemoryPeakBaseline(t *testing.T) {
	requireLongTests(t)
	for _, minutes := range []int{2, 10} {
		src := makeLongSource(t, minutes)
		p := testProject(src)
		p.Venue.Preset = "livehouse"
		mb := func(b uint64) float64 { return float64(b) / (1 << 20) }

		peak := peakHeap(func() {
			if _, err := Render(context.Background(), p, nil); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("Render  %2d min: HeapInuse peak %.0f MB", minutes, mb(peak))

		peak = peakHeap(func() {
			if _, err := NewEngine().Preview(context.Background(), p, nil); err != nil {
				t.Fatal(err)
			}
		})
		t.Logf("Preview %2d min: HeapInuse peak %.0f MB", minutes, mb(peak))
	}
}

func BenchmarkRender10Min(b *testing.B) {
	requireLongTests(b)
	p := testProject(makeLongSource(b, 10))
	p.Venue.Preset = "livehouse"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Render(context.Background(), p, nil); err != nil {
			b.Fatal(err)
		}
	}
}

// TestMemoryStaysFlat は、曲が長くなってもヒープのピークが増えないことを確かめる(合成音源。ffmpeg もディスクの音源も使わない)。
// 2分と20分の曲を、書き出し(RenderTo、キャッシュなし)とプレビュー(PreviewTo、キャッシュ・帯域レベルあり)で処理して、
// ピークの差と上限を判定する。
func TestMemoryStaysFlat(t *testing.T) {
	requireLongTests(t)
	mb := func(b uint64) float64 { return float64(b) / (1 << 20) }
	for _, mode := range []string{"RenderTo", "PreviewTo"} {
		measure := func(sec int) uint64 {
			e := &Engine{openSource: synthOpener}
			if mode == "PreviewTo" {
				e = NewEngine()
				e.openSource = synthOpener
				defer e.Close()
			}
			sink := &discardSink{}
			var res *Result
			peak := peakHeap(func() {
				var err error
				if res, err = e.RenderTo(context.Background(), synthProject(sec), nil, sink); err != nil {
					t.Fatal(err)
				}
			})
			if sink.frames != res.Frames || sink.frames < sec*sampleRate {
				t.Fatalf("%d s: sink got %d frames, result %d", sec, sink.frames, res.Frames)
			}
			t.Logf("%-9s %2d min: HeapInuse peak %.0f MB", mode, sec/60, mb(peak))
			return peak
		}
		short, long := measure(2*60), measure(20*60)
		if long > short && long-short >= 32<<20 {
			t.Errorf("%s: peak grew with the song length: %.0f MB -> %.0f MB", mode, mb(short), mb(long))
		}
		if long >= 300<<20 {
			t.Errorf("%s: peak %.0f MB is too large", mode, mb(long))
		}
	}
}
