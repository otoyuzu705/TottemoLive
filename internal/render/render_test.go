package render

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"tottemolive/internal/audio"
	"tottemolive/internal/project"
)

// テスト用の音源(3秒のモノラル。複数の周波数のバースト)をffmpegで作る。
func makeSource(t *testing.T, dir string) string {
	t.Helper()
	if !audio.Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	path := filepath.Join(dir, "src.wav")
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=3:sample_rate=44100",
		"-f", "lavfi", "-i", "sine=frequency=1800:duration=3:sample_rate=44100",
		"-filter_complex", "[0]volume=0.5[a];[1]volume=0.3,tremolo=f=4:d=1[b];[a][b]amix=inputs=2:normalize=0",
		path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return path
}

func testProject(src string) project.Project {
	p := project.New()
	p.Sources = []project.Source{{ID: "s", Path: src, Role: project.RoleMix}}
	p.Venue = project.DefaultVenue()
	p.Crowd.Keyframes = []project.Keyframe{{T: 0, Cheer: 0.8}, {T: 3, Cheer: 0.1}}
	p.Crowd.ClapRanges = []project.ClapRange{{Start: 1, End: 3}}
	return p
}

func TestRenderEndToEnd(t *testing.T) {
	dir := t.TempDir()
	p := testProject(makeSource(t, dir))

	stages := map[string]bool{}
	res, err := Render(context.Background(), p, func(stage string, ratio float64) { stages[stage] = true })
	if err != nil {
		t.Fatal(err)
	}
	if res.SampleRate != 48000 || len(res.Audio) != 2 || len(res.Audio[0]) != len(res.Audio[1]) {
		t.Fatalf("bad result shape")
	}
	// 3秒の素材 + 残響の尾(arena RT60 2.8s 付近)
	sec := float64(len(res.Audio[0])) / 48000
	if sec < 5 || sec > 9 {
		t.Errorf("length %.2fs", sec)
	}
	if !stages[StageDecode] || !stages[StageProcess] {
		t.Errorf("stages not reported: %v", stages)
	}
	// ラウドネスが目標、ピークが上限以下
	if math.Abs(res.LUFS-p.Output.TargetLufs) > 1 {
		t.Errorf("LUFS %.2f, target %.1f", res.LUFS, p.Output.TargetLufs)
	}
	peak := 0.0
	for _, ch := range res.Audio {
		for _, v := range ch {
			if math.IsNaN(float64(v)) {
				t.Fatal("NaN in output")
			}
			peak = math.Max(peak, math.Abs(float64(v)))
		}
	}
	if db := 20 * math.Log10(peak); db > p.Output.CeilingDbTp+0.01 {
		t.Errorf("peak %.2f dBFS above ceiling %.1f", db, p.Output.CeilingDbTp)
	}
	// 左右の耳で差がある(バイノーラル化されている)
	diff := 0.0
	for i := range res.Audio[0] {
		diff += math.Abs(float64(res.Audio[0][i] - res.Audio[1][i]))
	}
	if diff == 0 {
		t.Error("L and R identical")
	}
}

func TestExportWAV(t *testing.T) {
	dir := t.TempDir()
	p := testProject(makeSource(t, dir))
	p.Venue.Preset = "livehouse"
	out := filepath.Join(dir, "out.wav")
	if err := Export(context.Background(), p, out, nil); err != nil {
		t.Fatal(err)
	}
	info, err := audio.Probe(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 48000 || info.Channels != 2 || info.DurationSec < 3 {
		t.Errorf("output info %+v", info)
	}
	b, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", out).Output()
	if string(b) != "pcm_s24le\n" && string(b) != "pcm_s24le\r\n" {
		t.Errorf("codec %q", b)
	}
}

// パラメーターを変えると音が変わる(反映漏れの検出)。
func TestParametersAffectOutput(t *testing.T) {
	dir := t.TempDir()
	base := testProject(makeSource(t, dir))
	base.Venue.Preset = "livehouse" // 速く回すため残響の短い会場で
	render := func(p project.Project) [][]float32 {
		res, err := Render(context.Background(), p, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res.Audio
	}
	ref := render(base)
	for name, mod := range map[string]func(*project.Project){
		"pa.lowCutHz":      func(p *project.Project) { p.PA.LowCutHz = 200 },
		"pa.drive":         func(p *project.Project) { p.PA.Drive = 1 },
		"spatial.distance": func(p *project.Project) { p.Spatial.DistanceRolloff = 0 },
		"reverb.mix":       func(p *project.Project) { p.Reverb.Mix = 0.9 },
		"reverb.decay":     func(p *project.Project) { p.Reverb.DecayScale = 0.5 },
		"crowd.levelDb":    func(p *project.Project) { p.Crowd.LevelDb = 6 },
		"crowd.seed":       func(p *project.Project) { p.Crowd.Seed = 5 },
		"listener":         func(p *project.Project) { p.Listener.X = 5 },
	} {
		q := base.Clone()
		mod(&q)
		got := render(q)
		n := min(len(got[0]), len(ref[0]))
		d := 0.0
		for i := 0; i < n; i++ {
			d += math.Abs(float64(got[0][i] - ref[0][i]))
		}
		if d/float64(n) < 1e-5 {
			t.Errorf("%s: output unchanged", name)
		}
	}
}

// 処理段階に入った時点でキャンセルすると、エラーで止まる(処理の速さに依らない)。
func TestCancel(t *testing.T) {
	p := testProject(makeSource(t, t.TempDir()))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	_, err := Render(ctx, p, func(stage string, ratio float64) {
		if stage == StageProcess {
			once.Do(cancel)
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestErrors(t *testing.T) {
	p := project.New()
	if _, err := Render(context.Background(), p, nil); err == nil {
		t.Error("no sources should fail")
	}
	p.Sources = []project.Source{{ID: "x", Path: filepath.Join(os.TempDir(), "does-not-exist.wav")}}
	if audio.Available() {
		if _, err := Render(context.Background(), p, nil); err == nil {
			t.Error("missing file should fail")
		}
	}
	p.Venue.Preset = "nope"
	if _, err := Render(context.Background(), p, nil); err == nil {
		t.Error("unknown venue should fail")
	}
}
