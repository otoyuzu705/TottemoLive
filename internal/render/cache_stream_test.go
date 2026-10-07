package render

import (
	"context"
	"math"
	"os"
	"sync"
	"testing"

	"tottemolive/internal/project"
)

// synthEngine は、合成音源を使うキャッシュ付きエンジン(プレビュー用)。
func synthEngine(t *testing.T, chunk int) *Engine {
	e := newTestEngine(t)
	e.openSource = synthOpener
	e.chunk = chunk
	return e
}

// synthTwoProject は、長さの違う2つの合成音源のプロジェクト。
func synthTwoProject() project.Project {
	p := synthProject(4)
	p.Sources = append(p.Sources, project.Source{ID: "t", Path: "synth:3", Role: project.RoleMix, GainDb: -3})
	return p
}

func sameAudio(t *testing.T, name string, a, b *Result) {
	t.Helper()
	if m, n := compareAudio(t, name, a.Audio, b.Audio); m != 0 || n != 0 {
		t.Fatalf("%s: audio differs", name)
	}
	if a.LUFS != b.LUFS {
		t.Fatalf("%s: LUFS %v vs %v", name, a.LUFS, b.LUFS)
	}
}

// キャッシュに当たった段(スプールから読む)を使った結果は、キャッシュなしで一から計算した結果と同じ。
func TestCachedStagesMatchFreshRender(t *testing.T) {
	ctx := context.Background()
	base := synthTwoProject()
	e := synthEngine(t, 0)
	defer e.Close()
	if _, err := e.Preview(ctx, base, nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		mod  func(*project.Project)
	}{
		{"reverb.mix(直接音・残響・帯域がキャッシュ)", func(p *project.Project) { p.Reverb.Mix = 0.7 }},
		{"output.targetLufs", func(p *project.Project) { p.Output.TargetLufs = -18 }},
		{"listener(残響だけキャッシュ)", func(p *project.Project) { p.Listener.X = 4 }},
		{"reverb.decayScale(直接音だけキャッシュ)", func(p *project.Project) { p.Reverb.DecayScale = 0.7 }},
		{"sub.levelDb", func(p *project.Project) { p.Sub.LevelDb = 6 }},
		{"pa.lowShelfDb(PAから)", func(p *project.Project) { p.PA.LowShelfDb = 5 }},
	}
	for _, tc := range cases {
		q := base.Clone()
		tc.mod(&q)
		got, err := e.Preview(ctx, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		want, err := (&Engine{openSource: synthOpener}).renderMem(ctx, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		sameAudio(t, tc.name, got, want)
		if got.PA == nil || got.PA.Series == nil {
			t.Fatalf("%s: no PA spectrum", tc.name)
		}
		// PA出力の帯域レベルも、一から計算した結果と同じ
		fresh, err := synthEngine(t, 0).Preview(ctx, q, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(fresh.PA.Series.Data) != len(got.PA.Series.Data) || fresh.PA.OffsetDb != got.PA.OffsetDb {
			t.Fatalf("%s: PA spectrum shape/offset differs", tc.name)
		}
		for i, v := range fresh.PA.Series.Data {
			if got.PA.Series.Data[i] != v {
				t.Fatalf("%s: PA spectrum differs at %d", tc.name, i)
			}
		}
	}
}

// プレビューの結果(音声・ラウドネス・帯域レベル・オフセット)は、チャンクの大きさに依らない。
func TestPreviewChunkInvariance(t *testing.T) {
	ctx := context.Background()
	p := synthTwoProject()
	var ref *Result
	for _, chunk := range []int{997, 4096, 65536, 1 << 20} {
		e := synthEngine(t, chunk)
		got, err := e.Preview(ctx, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		// 2回目(全段がキャッシュ)も同じ
		again, err := e.Preview(ctx, p, nil)
		if err != nil {
			t.Fatal(err)
		}
		sameAudio(t, "cache hit", again, got)
		e.Close()
		if ref == nil {
			ref = got
			continue
		}
		sameAudio(t, "chunk", got, ref)
		if got.PA.OffsetDb != ref.PA.OffsetDb || got.PA.Series.Frames != ref.PA.Series.Frames {
			t.Fatalf("chunk %d: PA spectrum offset/frames differ", chunk)
		}
		for i, v := range ref.PA.Series.Data {
			if got.PA.Series.Data[i] != v {
				t.Fatalf("chunk %d: PA series differs at %d", chunk, i)
			}
		}
	}
}

func spoolFiles(t *testing.T, e *Engine) int {
	t.Helper()
	e.dirMu.Lock()
	dir := e.dir
	e.dirMu.Unlock()
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// 段の出力が入れ替わると、古いスプールは消える。一時ディレクトリのファイルは、PA・直接音・残響の3つ(+計算中のミックス1つ)以下。
func TestSpoolsAreReplacedAndRemoved(t *testing.T) {
	ctx := context.Background()
	base := synthTwoProject()
	e := synthEngine(t, 0)
	if _, err := e.Preview(ctx, base, nil); err != nil {
		t.Fatal(err)
	}
	if n := spoolFiles(t, e); n != 3 {
		t.Fatalf("after the first preview there are %d spool files, want 3 (pa, direct, reverb)", n)
	}
	for i, mod := range []func(*project.Project){
		func(p *project.Project) { p.PA.LowShelfDb = 4 },
		func(p *project.Project) { p.Listener.X = 6 },
		func(p *project.Project) { p.Reverb.DecayScale = 0.5 },
		func(p *project.Project) { p.Reverb.Mix = 0.2 },
		func(p *project.Project) { p.PA.CompRatio = 6 },
	} {
		q := base.Clone()
		mod(&q)
		if _, err := e.Preview(ctx, q, nil); err != nil {
			t.Fatal(err)
		}
		if n := spoolFiles(t, e); n > 4 {
			t.Fatalf("step %d: %d spool files remain", i, n)
		}
	}
	dir := e.dir
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if fileExists(dir) {
		t.Error("temp dir remains after Close")
	}
}

// 中断されたら、書きかけのスプールは残らない。計算済みの段は残る。
func TestCancelLeavesNoPartialSpools(t *testing.T) {
	base := synthTwoProject()
	e := synthEngine(t, 997)
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	_, err := e.Preview(ctx, base, func(stage string, ratio float64) {
		if stage == StageProcess && ratio > 0.3 {
			once.Do(cancel)
		}
	})
	if err != context.Canceled {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if n := spoolFiles(t, e); n != 0 {
		t.Errorf("%d spool files remain after the job was cancelled", n)
	}
	// 続けて普通に動く
	if _, err := e.Preview(context.Background(), base, nil); err != nil {
		t.Fatal(err)
	}
	if n := spoolFiles(t, e); n != 3 {
		t.Errorf("%d spool files after a normal preview", n)
	}
	// 書き出し(キャッシュなし)の中断でも、一時ファイルは残らない
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var once2 sync.Once
	scratch := t.TempDir()
	_, err = (&Engine{openSource: synthOpener, chunk: 997, baseDir: scratch}).RenderTo(ctx2, base, func(stage string, ratio float64) {
		if stage == StageEncode && ratio > 0.3 {
			once2.Do(cancel2)
		}
	}, &discardSink{})
	if err != context.Canceled {
		t.Fatalf("export cancel: %v", err)
	}
	if n := dirEntries(t, scratch); n != 0 {
		t.Errorf("%d entries remain in the scratch dir after the cancelled export", n)
	}
}

// スペクトラム(PA出力の帯域レベル)だけが外れた場合(直接音・残響はキャッシュ)も、結果が合う。
func TestSpectrumOnlyMiss(t *testing.T) {
	ctx := context.Background()
	p := synthTwoProject()
	e := synthEngine(t, 0)
	defer e.Close()
	e.analyzePA = false
	first, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.PA != nil {
		t.Fatal("PA spectrum without analyzePA")
	}
	e.analyzePA = true
	second, err := e.Preview(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.PA == nil || math.IsNaN(second.PA.OffsetDb) {
		t.Fatalf("spectrum missing: %+v", second.PA)
	}
	sameAudio(t, "spectrum miss", second, first)
	st := e.Stats()
	if st["direct"].Computed != 1 || st["reverb"].Computed != 1 || st["pa"].Computed != 1 {
		t.Errorf("stages recomputed: %+v", st)
	}
}
