package crowd

import (
	"context"
	"math"
	"reflect"
	"testing"

	"livebin/internal/project"
	"livebin/internal/spatial"
)

const sr = 48000

func rmsRange(x []float32, from, to float64) float64 {
	a, b := int(from*sr), int(to*sr)
	s := 0.0
	for _, v := range x[a:b] {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / float64(b-a))
}

func setup(t *testing.T) (project.Project, spatial.Set) {
	set, err := spatial.LoadSet("synthetic", sr)
	if err != nil {
		t.Fatal(err)
	}
	return project.New(), set
}

func TestSilentWithoutKeyframesOrClaps(t *testing.T) {
	p, set := setup(t)
	out, err := Render(context.Background(), p.Crowd, p.Listener, set, sr, sr)
	if err != nil {
		t.Fatal(err)
	}
	if rmsRange(out[0], 0, 1) != 0 {
		t.Error("expected silence")
	}
	p.Crowd.Density = 0
	p.Crowd.Keyframes = []project.Keyframe{{T: 0, Cheer: 1}}
	out, _ = Render(context.Background(), p.Crowd, p.Listener, set, sr, sr)
	if rmsRange(out[0], 0, 1) != 0 {
		t.Error("density 0 should be silent")
	}
}

func TestCheerFollowsKeyframes(t *testing.T) {
	p, set := setup(t)
	p.Crowd.Keyframes = []project.Keyframe{{T: 0, Cheer: 1}, {T: 2, Cheer: 1}, {T: 3, Cheer: 0}}
	out, err := Render(context.Background(), p.Crowd, p.Listener, set, sr, 5*sr)
	if err != nil {
		t.Fatal(err)
	}
	loud, quiet := rmsRange(out[0], 0.5, 1.5), rmsRange(out[0], 3.5, 4.5)
	if loud == 0 || quiet != 0 {
		t.Errorf("loud=%v quiet=%v", loud, quiet)
	}
}

func TestClapsOnlyInRange(t *testing.T) {
	p, set := setup(t)
	p.Crowd.ClapRanges = []project.ClapRange{{Start: 1, End: 3}}
	out, err := Render(context.Background(), p.Crowd, p.Listener, set, sr, 5*sr)
	if err != nil {
		t.Fatal(err)
	}
	if rmsRange(out[1], 1.2, 3) == 0 || rmsRange(out[1], 3.6, 5) != 0 {
		t.Error("claps should only sound within the range")
	}
}

func TestDeterministicAndSeedDependent(t *testing.T) {
	p, set := setup(t)
	p.Crowd.Keyframes = []project.Keyframe{{T: 0, Cheer: 1}}
	a, _ := Render(context.Background(), p.Crowd, p.Listener, set, sr, sr)
	b, _ := Render(context.Background(), p.Crowd, p.Listener, set, sr, sr)
	if !reflect.DeepEqual(a, b) {
		t.Error("not deterministic")
	}
	p.Crowd.Seed = 2
	c, _ := Render(context.Background(), p.Crowd, p.Listener, set, sr, sr)
	if reflect.DeepEqual(a, c) {
		t.Error("seed has no effect")
	}
}
