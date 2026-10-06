package render

import (
	"os/exec"
	"path/filepath"
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
