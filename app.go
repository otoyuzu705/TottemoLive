package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"tottemolive/assets"
	"tottemolive/internal/audio"
	"tottemolive/internal/params"
	"tottemolive/internal/project"
	"tottemolive/internal/render"
	"tottemolive/internal/separate"
	"tottemolive/internal/venue"
)

// App はフロントに公開するメソッドを持つ。Wailsに依存してよいのは main.go とこのファイルだけ。
// 重い処理はすべて internal/render が行い、ここは呼び出しと進捗イベントの橋渡しだけをする。
type App struct {
	ctx     context.Context
	emit    func(event string, data any) // テストで差し替える
	engine  *render.Engine
	jobs    *render.Jobs
	store   *render.Store
	presets *project.PresetStore
	stemDir string // ステム分離の結果のキャッシュ

	mu      sync.Mutex
	nextSrc int
	sources map[string]string // 音源ID → パス(GetPeaks 用)
}

// ユーザーデータ(プリセット・キャッシュ)を置くフォルダ名。legacyAppDir はプロジェクト名を変える前の名前。
const (
	appDir       = "TottemoLive"
	legacyAppDir = "livebin"
)

// migrateDir は、旧フォルダがあり新フォルダが無いときだけ、旧フォルダを新しい場所へ移す。
// 失敗しても起動は続ける(その場合は新しい名前で空の状態から始まる)。
func migrateDir(oldDir, newDir string) {
	if _, err := os.Stat(newDir); err == nil {
		return
	}
	if _, err := os.Stat(oldDir); err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return
	}
	_ = os.Rename(oldDir, newDir)
}

func NewApp() *App {
	userDir, err := os.UserConfigDir()
	if err != nil {
		userDir = os.TempDir()
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	// 旧名(livebin)の時期に保存したプリセットとキャッシュを、新しい名前のフォルダへ引き継ぐ
	migrateDir(filepath.Join(userDir, legacyAppDir, "presets"), filepath.Join(userDir, appDir, "presets"))
	migrateDir(filepath.Join(cacheDir, legacyAppDir, "stems"), filepath.Join(cacheDir, appDir, "stems"))
	return &App{
		stemDir: filepath.Join(cacheDir, appDir, "stems"),
		engine:  render.NewEngine(),
		jobs:    render.NewJobs(),
		store:   render.NewStore(3),
		presets: &project.PresetStore{UserDir: filepath.Join(userDir, appDir, "presets"), Shipped: assets.ShippedPresets()},
		sources: map[string]string{},
		ctx:     context.Background(),
		emit:    func(string, any) {},
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emit = func(event string, data any) { runtime.EventsEmit(ctx, event, data) }
}

// --- イベントの中身(フロントは render:progress / render:done / render:error を購読する) ---

type ProgressEvent struct {
	JobID string  `json:"jobId"`
	Stage string  `json:"stage"` // decode / process / encode
	Ratio float64 `json:"ratio"`
}

type DoneEvent struct {
	JobID string `json:"jobId"`
	Path  string `json:"path"`
}

type ErrorEvent struct {
	JobID   string `json:"jobId"`
	Message string `json:"message"`
}

type SeparateProgressEvent struct {
	JobID    string  `json:"jobId"`
	SourceID string  `json:"sourceId"`
	Ratio    float64 `json:"ratio"`
}

// SeparateDoneEvent は分離の完了。Vocals と Backing は取り込み済みの音源(新しいID)。
type SeparateDoneEvent struct {
	JobID    string     `json:"jobId"`
	SourceID string     `json:"sourceId"`
	Vocals   SourceInfo `json:"vocals"`
	Backing  SourceInfo `json:"backing"`
}

type SeparateErrorEvent struct {
	JobID    string `json:"jobId"`
	SourceID string `json:"sourceId"`
	Message  string `json:"message"`
}

// --- 素材 ---

// SourceInfo は取り込んだ音源の情報。ID はこの起動の間だけ有効で、プロジェクトの Source.ID に使う。
type SourceInfo struct {
	ID          string  `json:"id"`
	Path        string  `json:"path"`
	Name        string  `json:"name"`
	DurationSec float64 `json:"durationSec"`
	SampleRate  int     `json:"sampleRate"`
	Channels    int     `json:"channels"`
}

var audioFilter = runtime.FileFilter{DisplayName: "音声ファイル (*.wav, *.flac, *.mp3, *.m4a)", Pattern: "*.wav;*.flac;*.mp3;*.m4a"}

// OpenAudioFiles はネイティブのファイル選択を開き、選ばれた音源の長さ・サンプルレートを返す。
func (a *App) OpenAudioFiles() ([]SourceInfo, error) {
	paths, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "音源を選択", Filters: []runtime.FileFilter{audioFilter},
	})
	if err != nil || len(paths) == 0 {
		return []SourceInfo{}, err
	}
	return a.AddAudioFiles(paths)
}

// AddAudioFiles はパスで指定された音源を取り込む(ドラッグ&ドロップ用)。
// 読めないファイルがあっても他は取り込み、エラーにまとめて返す。
func (a *App) AddAudioFiles(paths []string) ([]SourceInfo, error) {
	out := []SourceInfo{}
	var errs []error
	for _, path := range paths {
		info, err := audio.Probe(a.ctx, path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		a.mu.Lock()
		a.nextSrc++
		id := fmt.Sprintf("src%d", a.nextSrc)
		a.sources[id] = path
		a.mu.Unlock()
		out = append(out, SourceInfo{ID: id, Path: path, Name: filepath.Base(path),
			DurationSec: info.DurationSec, SampleRate: info.SampleRate, Channels: info.Channels})
	}
	return out, errors.Join(errs...)
}

// StemSeparationAvailable はDemucsが使えるか(任意機能)。使えないときフロントは分離ボタンを隠す。
func (a *App) StemSeparationAvailable() bool { return separate.Available() }

// SeparateSource は音源をボーカルと伴奏に分離するジョブを開始してジョブIDを返す。
// 進捗は separate:progress、完了は separate:done(分離した2本は取り込み済み)、
// 失敗・中断は separate:error で通知する。中断は CancelJob。同じ音源の結果はキャッシュされる。
func (a *App) SeparateSource(sourceID string) (string, error) {
	a.mu.Lock()
	path, ok := a.sources[sourceID]
	a.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("不明な音源ID %q", sourceID)
	}
	if !separate.Available() {
		return "", errors.New("Demucs が見つかりません(PATH に demucs を入れるか、環境変数 TOTTEMOLIVE_DEMUCS で場所を指定してください)")
	}
	id, ctx, done := a.jobs.Begin(a.ctx, "")
	go func() {
		defer done()
		var last float64
		stems, err := separate.Separate(ctx, path, a.stemDir, func(r float64) {
			if r-last >= 0.01 || r >= 1 {
				last = r
				a.emit("separate:progress", SeparateProgressEvent{JobID: id, SourceID: sourceID, Ratio: r})
			}
		})
		if err != nil {
			msg := err.Error()
			if errors.Is(err, context.Canceled) {
				msg = "中断しました"
			}
			a.emit("separate:error", SeparateErrorEvent{JobID: id, SourceID: sourceID, Message: msg})
			return
		}
		infos, err := a.AddAudioFiles([]string{stems.Vocals, stems.Backing})
		if err != nil || len(infos) != 2 {
			a.emit("separate:error", SeparateErrorEvent{JobID: id, SourceID: sourceID, Message: fmt.Sprintf("分離した音源を読み込めません: %v", err)})
			return
		}
		a.emit("separate:done", SeparateDoneEvent{JobID: id, SourceID: sourceID, Vocals: infos[0], Backing: infos[1]})
	}()
	return id, nil
}

// GetPeaks は波形表示用のmin/maxピーク列(長さ 2*width)を返す。描画はフロントのcanvas。
func (a *App) GetPeaks(sourceID string, width int) ([]float32, error) {
	a.mu.Lock()
	path, ok := a.sources[sourceID]
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("不明な音源ID %q", sourceID)
	}
	return audio.Peaks(a.ctx, path, width)
}

// --- プロジェクト ---

func (a *App) NewProject() project.Project { return project.New() }

// LoadProject はプロジェクトを読み、含まれる音源を GetPeaks で使えるよう登録する。
func (a *App) LoadProject(path string) (project.Project, error) {
	p, err := project.Load(path)
	if err != nil {
		return project.Project{}, err
	}
	a.mu.Lock()
	for _, s := range p.Sources {
		a.sources[s.ID] = s.Path
	}
	a.mu.Unlock()
	return p, nil
}

func (a *App) SaveProject(path string, p project.Project) error { return project.Save(path, p) }

var projectFilter = runtime.FileFilter{DisplayName: "TottemoLiveプロジェクト (*.json)", Pattern: "*.json"}

// ChooseProjectToOpen は開くプロジェクトファイルを選ぶ。キャンセルは空文字。
func (a *App) ChooseProjectToOpen() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "プロジェクトを開く", Filters: []runtime.FileFilter{projectFilter},
	})
}

// ChooseProjectSavePath はプロジェクトの保存先を選ぶ。キャンセルは空文字。
func (a *App) ChooseProjectSavePath() (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "プロジェクトを保存", DefaultFilename: "project.json", Filters: []runtime.FileFilter{projectFilter},
	})
}

// ChooseExportPath は書き出し先のWAVを選ぶ。キャンセルは空文字。
func (a *App) ChooseExportPath() (string, error) {
	return runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title: "書き出し先", DefaultFilename: "TottemoLive.wav",
		Filters: []runtime.FileFilter{{DisplayName: "WAV (*.wav)", Pattern: "*.wav"}},
	})
}

// --- 音作りパラメーター・会場・プリセット ---

func (a *App) ListParams() []params.ParamSpec { return params.ListParams() }

func (a *App) ListVenues() []venue.Preset { return venue.List() }

func (a *App) ApplyVenue(p project.Project, presetID string) (project.Project, error) {
	return venue.Apply(p, presetID)
}

func (a *App) ListSoundPresets() ([]project.SoundPresetInfo, error) {
	list, err := a.presets.List()
	if list == nil {
		list = []project.SoundPresetInfo{}
	}
	return list, err
}

func (a *App) ApplySoundPreset(p project.Project, name string) (project.Project, error) {
	sp, err := a.presets.Load(name)
	if err != nil {
		return p, err
	}
	return p.ApplySoundPreset(sp), nil
}

func (a *App) SaveSoundPreset(name string, p project.Project) error {
	return a.presets.Save(name, project.ExtractSoundPreset(p))
}

func (a *App) DeleteSoundPreset(name string) error { return a.presets.Delete(name) }

// --- レンダリング ---

// PreviewResult はプレビューのレンダリング結果。URL が空なら、新しい要求に追い越されて中断された
// (フロントは無視する)。BandsURL はPA出力の帯域レベル(リトルエンディアンの float32、フレーム × Bands の行優先、
// dB、0 dBFS の正弦波 = 0 dB)で、フレーム f の中心は f × HopSec 秒。OffsetDb は、PA出力の全体の大きさを
// 耳に届く出力にそろえる値(dB)。スペクトラム表示で「PAから出た音」を重ねるために使う。
type PreviewResult struct {
	URL      string  `json:"url"`
	BandsURL string  `json:"bandsUrl"`
	Bands    int     `json:"bands"`
	Frames   int     `json:"frames"`
	HopSec   float64 `json:"hopSec"`
	OffsetDb float64 `json:"offsetDb"`
}

// RenderPreview は曲全体を書き出しと同じ処理でレンダリングし、プレビューのURL(/preview/{id}.wav)と、
// PA出力の帯域レベルのURL(/preview/{id}.bands)を返す。段ごとのキャッシュを使う。
// 新しい要求が来ると進行中のプレビューは中断され、中断された呼び出しは URL が空の結果とnilを返す。
func (a *App) RenderPreview(p project.Project) (PreviewResult, error) {
	_, ctx, done := a.jobs.Begin(a.ctx, "preview")
	defer done()
	res, err := a.engine.Preview(ctx, p, nil)
	if err != nil {
		if ctx.Err() != nil {
			return PreviewResult{}, nil
		}
		return PreviewResult{}, err
	}
	wav := audio.WAV16(res.Audio, res.SampleRate)
	if res.PA == nil {
		return PreviewResult{URL: a.store.Put(wav)}, nil
	}
	url, bandsURL := a.store.PutWithBands(wav, res.PA.Series.Bytes())
	return PreviewResult{
		URL: url, BandsURL: bandsURL,
		Bands: res.PA.Series.Bands, Frames: res.PA.Series.Frames, HopSec: res.PA.Series.HopSec, OffsetDb: res.PA.OffsetDb,
	}, nil
}

// WindowResult は先行プレビュー(曲の一部だけを先に処理した結果)。URL が空なら、新しい要求に追い越されて中断された
// (フロントは無視する)。StartSec は URL の音の先頭が曲頭から何秒の位置か、TotalSec は曲全体(残響の尾を含む)の長さ(秒)。
type WindowResult struct {
	URL      string  `json:"url"`
	StartSec float64 `json:"startSec"`
	TotalSec float64 `json:"totalSec"`
}

// RenderPreviewWindow は、startSec(曲頭からの秒)から約30秒ぶんだけを、曲全体と同じ処理で先にレンダリングする。
// 曲全体の処理(RenderPreview)が終わるまでの間、シーク位置の周辺をすぐに聴けるようにするためのもの。
// supersede が true なら、進行中の曲全体のプレビューを中断する(パラメーターが変わって、古い値の処理が不要になったとき)。
// false なら、曲全体の処理はそのまま続ける(同じ値のまま、窓の外へシークしたとき)。
// 新しい窓の要求が来ると、進行中の窓は中断され、中断された呼び出しは URL が空の結果とnilを返す。
func (a *App) RenderPreviewWindow(p project.Project, startSec float64, supersede bool) (WindowResult, error) {
	if supersede {
		a.jobs.CancelKind("preview")
	}
	_, ctx, done := a.jobs.Begin(a.ctx, "previewWindow")
	defer done()
	w, err := a.engine.PreviewWindow(ctx, p, startSec)
	if err != nil {
		if ctx.Err() != nil {
			return WindowResult{}, nil
		}
		return WindowResult{}, err
	}
	return WindowResult{URL: a.store.Put(audio.WAV16(w.Audio, w.SampleRate)), StartSec: w.StartSec, TotalSec: w.TotalSec}, nil
}

// RenderOriginal は曲全体の原音(A/B比較用)のURLを返す。ラウドネスはプレビューと同じ目標にそろえる。
func (a *App) RenderOriginal(p project.Project) (string, error) {
	_, ctx, done := a.jobs.Begin(a.ctx, "original")
	defer done()
	res, err := a.engine.Original(ctx, p)
	return a.previewURL(ctx, res, err)
}

func (a *App) previewURL(ctx context.Context, res *render.Result, err error) (string, error) {
	if err != nil {
		if ctx.Err() != nil {
			return "", nil
		}
		return "", err
	}
	return a.store.Put(audio.WAV16(res.Audio, res.SampleRate)), nil
}

// StartExport は書き出しジョブを開始してジョブIDを返す。
// 進捗は render:progress、完了は render:done、失敗は render:error(中断も render:error)で通知する。
func (a *App) StartExport(p project.Project, outPath string) (string, error) {
	if outPath == "" {
		return "", errors.New("書き出し先が指定されていません")
	}
	id, ctx, done := a.jobs.Begin(a.ctx, "")
	p = p.Clone()
	go func() {
		defer done()
		err := render.Export(ctx, p, outPath, a.progressEmitter(id))
		switch {
		case errors.Is(err, context.Canceled):
			os.Remove(outPath) // 書きかけを残さない
			a.emit("render:error", ErrorEvent{JobID: id, Message: "中断しました"})
		case err != nil:
			os.Remove(outPath)
			a.emit("render:error", ErrorEvent{JobID: id, Message: err.Error()})
		default:
			a.emit("render:done", DoneEvent{JobID: id, Path: outPath})
		}
	}()
	return id, nil
}

func (a *App) CancelJob(jobID string) { a.jobs.Cancel(jobID) }

// progressEmitter は進捗イベントを間引いて送る(段が変わったとき、または1%以上進んだか50ms経ったとき)。
func (a *App) progressEmitter(jobID string) render.Progress {
	var mu sync.Mutex
	var lastStage string
	var lastRatio float64
	var lastAt time.Time
	return func(stage string, ratio float64) {
		mu.Lock()
		send := stage != lastStage || ratio >= 1 || ratio-lastRatio >= 0.01 || time.Since(lastAt) >= 50*time.Millisecond
		if send {
			lastStage, lastRatio, lastAt = stage, ratio, time.Now()
		}
		mu.Unlock()
		if send {
			a.emit("render:progress", ProgressEvent{JobID: jobID, Stage: stage, Ratio: ratio})
		}
	}
}
