package render

import (
	"context"
	"io"
	"math"
	"math/rand"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"tottemolive/internal/audio"
	"tottemolive/internal/project"
)

// makeTwoSources は、長さもチャンネル数も違う2つの音源(ステレオ3.0秒、モノ2.2秒)をffmpegで作る。
func makeTwoSources(t *testing.T) (stereo, mono string) {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	dir := t.TempDir()
	stereo = filepath.Join(dir, "stereo.wav")
	mono = filepath.Join(dir, "mono.wav")
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
	}
	run("-f", "lavfi", "-i", "sine=frequency=330:duration=3:sample_rate=44100",
		"-f", "lavfi", "-i", "sine=frequency=2200:duration=3:sample_rate=44100",
		"-filter_complex", "[0]volume=0.4,pan=stereo|c0=c0|c1=0.5*c0[a];[1]volume=0.2,tremolo=f=5:d=1,pan=stereo|c0=0.3*c0|c1=c0[b];[a][b]amix=inputs=2:normalize=0",
		stereo)
	run("-f", "lavfi", "-i", "sine=frequency=70:duration=2.2:sample_rate=48000",
		"-f", "lavfi", "-i", "sine=frequency=900:duration=2.2:sample_rate=48000",
		"-filter_complex", "[0]volume=0.5[a];[1]volume=0.3,tremolo=f=3:d=1[b];[a][b]amix=inputs=2:normalize=0",
		"-ac", "1", mono)
	return stereo, mono
}

// twoSourceProject は makeTwoSources の2音源を使うプロジェクト(会場は livehouse)。
func twoSourceProject(t *testing.T) project.Project {
	t.Helper()
	a, b := makeTwoSources(t)
	p := project.New()
	p.Sources = []project.Source{
		{ID: "a", Path: a, Role: project.RoleMix, GainDb: -2},
		{ID: "b", Path: b, Role: project.RoleMix, GainDb: 1.5},
	}
	p.Venue = project.DefaultVenue()
	p.Venue.Preset = "livehouse"
	return p
}

// synthSource は、ffmpeg もディスクも使わない合成音源(決まった乱数のノイズ + 変調したトーン)。Engine.openSource に差し込む。
type synthSource struct {
	frames, pos int
	rng         *rand.Rand
}

func newSynthSource(frames int, seed int64) *synthSource {
	return &synthSource{frames: frames, rng: rand.New(rand.NewSource(seed))}
}

func (s *synthSource) Read(dst [][]float32) (int, error) {
	n := min(len(dst[0]), s.frames-s.pos)
	if n <= 0 {
		return 0, io.EOF
	}
	for i := 0; i < n; i++ {
		t := float64(s.pos+i) / sampleRate
		v := float32(0.3*math.Sin(2*math.Pi*440*t)*(0.6+0.4*math.Sin(2*math.Pi*0.5*t))) + (s.rng.Float32()*2-1)*0.15
		dst[0][i] = v
		dst[1][i] = 0.7 * v
	}
	s.pos += n
	return n, nil
}

func (s *synthSource) ExpectedFrames() int { return s.frames }
func (s *synthSource) Close() error        { return nil }

// synthOpener は、パスが "synth:<秒>" の音源を合成音源として開く関数(Engine.openSource 用)。
func synthOpener(ctx context.Context, path string, sr int, progress func(float64)) (frameReader, error) {
	sec, err := strconv.Atoi(strings.TrimPrefix(path, "synth:"))
	if err != nil {
		return nil, err
	}
	return newSynthSource(sec*sr, int64(sec)), nil
}

// synthProject は、長さ sec 秒の合成音源1本のプロジェクト(会場は livehouse)。
func synthProject(sec int) project.Project {
	p := project.New()
	p.Sources = []project.Source{{ID: "s", Path: "synth:" + strconv.Itoa(sec), Role: project.RoleMix}}
	p.Venue = project.DefaultVenue()
	p.Venue.Preset = "livehouse"
	return p
}

// discardSink は出力を捨てて、フレーム数だけ数える。
type discardSink struct{ frames int }

func (d *discardSink) Start(frames, sampleRate int) error { return nil }
func (d *discardSink) Write(buf [][]float32) error        { d.frames += len(buf[0]); return nil }

// newTestEngine はキャッシュ付きのエンジンを返す。テストの終わりに Close して、一時ファイルを残さない。
func newTestEngine(t testing.TB) *Engine {
	t.Helper()
	e := NewEngine()
	t.Cleanup(func() { e.Close() })
	return e
}
