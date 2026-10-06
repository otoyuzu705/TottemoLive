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
	"tottemolive/internal/project"
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

// TestMemoryPeakBaseline は、曲の長さに対するヒープのピークを測る(書き出し経路 Render とプレビュー経路 Preview)。
// 判定はせず、数字を記録する。長い曲でも一定に収まることを確かめる判定は TestMemoryStaysFlat。
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

var _ = project.New
