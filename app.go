package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
	"tottemolive/internal/settings"
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

	// 設定(キャッシュを使うか・置き場所)。変更は setMu で直列にする。
	// cfgMu は engine / store の差し替えを守る: プレビュー系のジョブは実行中ずっと読み側(RLock)を持ち、
	// 設定の適用は書き側(Lock)で、実行中のプレビュー系ジョブがすべて終わってから差し替える。
	settingsPath string
	setMu        sync.Mutex
	cfgMu        sync.RWMutex
	exports      sync.WaitGroup // 実行中の書き出しジョブ(終了時に待つ)

	// previewHook は、曲全体のプレビューの実行中(ジョブの登録後・レンダリングの前)に呼ばれるテスト用のフック(通常は nil)。
	previewHook func(ctx context.Context)

	mu       sync.Mutex
	settings settings.Settings // mu で守る。現在有効な設定
	nextSrc  int
	sources  map[string]string // 音源ID → パス(GetPeaks 用)
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
	return newAppAt(userDir, cacheDir)
}

// newAppAt は userDir(設定・プリセット)と cacheDir(ステム分離の結果)の下にユーザーデータを置くアプリを作る(テストで差し替える)。
func newAppAt(userDir, cacheDir string) *App {
	settingsPath := filepath.Join(userDir, appDir, "settings.json")
	cfg := settings.Load(settingsPath)
	// 保存した置き場所が使えない(外付けドライブが無いなど)ときは、このセッションだけ既定の場所で起動する
	// (ファイルは書き換えない。設定ダイアログで選び直せる)
	if v, err := settings.Validate(cfg); err == nil {
		cfg = v
	} else {
		cfg.CacheDir = ""
	}
	return &App{
		stemDir:      filepath.Join(cacheDir, appDir, "stems"),
		engine:       render.NewEngineWith(engineConfig(cfg)),
		jobs:         render.NewJobs(),
		store:        render.NewStoreIn(previewKeep, cfg.CacheDir),
		presets:      &project.PresetStore{UserDir: filepath.Join(userDir, appDir, "presets"), Shipped: assets.ShippedPresets()},
		settingsPath: settingsPath,
		settings:     cfg,
		sources:      map[string]string{},
		ctx:          context.Background(),
		emit:         func(string, any) {},
	}
}

// previewKeep はプレビューのWAVを保持する件数(現在のプレビュー・原音・次のプレビュー)。
const previewKeep = 3

func engineConfig(s settings.Settings) render.EngineConfig {
	return render.EngineConfig{CacheEnabled: s.CacheEnabled, Dir: s.CacheDir}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.emit = func(event string, data any) { runtime.EventsEmit(ctx, event, data) }
	// 異常終了などで残った古い一時ファイル(プレビュー・レンダリングの途中のもの)を掃除する。
	// 設定した置き場所と、OS の一時フォルダ(置き場所を変える前に残ったもの)の両方を見る
	dir := a.currentSettings().CacheDir
	go func() {
		render.CleanStaleTempIn(dir, 24*time.Hour)
		if dir != "" {
			render.CleanStaleTemp(24 * time.Hour)
		}
	}()
}

// serveAssets は /preview/{id}.wav などを、現在のプレビューのストアから配信する(設定で置き場所を変えると、ストアは差し替わる)。
func (a *App) serveAssets(w http.ResponseWriter, r *http.Request) {
	a.cfgMu.RLock()
	st := a.store
	a.cfgMu.RUnlock()
	st.ServeHTTP(w, r)
}

func (a *App) currentSettings() settings.Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings
}

// shutdown はアプリの終了時に、実行中のジョブをすべて中断し(書き出し中の ffmpeg も止まり、書きかけの出力は消える)、
// プレビューの一時ファイル(段のキャッシュ・配信中のWAV)を消す。
// engine / store は設定の適用(applyCacheSettings)が差し替えるので、cfgMu の書き側のロックを取ってから閉じる
// (プレビュー系のジョブは、中断されて読み側のロックを放すまで待つ)。
func (a *App) shutdown(ctx context.Context) {
	a.jobs.CancelAll()
	exported := make(chan struct{})
	go func() { a.exports.Wait(); close(exported) }()
	select {
	case <-exported:
	case <-time.After(10 * time.Second): // 子プロセスが止まらなくても、終了は妨げない
	}
	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()
	a.engine.Close()
	a.store.Close()
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
	a.cfgMu.RLock() // 設定の適用(engine・store の差し替え)を、この呼び出しが終わるまで待たせる
	defer a.cfgMu.RUnlock()
	_, ctx, done := a.jobs.Begin(a.ctx, "preview")
	defer done()
	if a.previewHook != nil {
		a.previewHook(ctx)
	}
	// WAVは、曲全体をメモリに持たず、できた分から一時ファイルへ書く
	w := a.store.NewWAV()
	res, err := a.engine.PreviewTo(ctx, p, nil, w)
	if err != nil {
		w.Abort()
		if ctx.Err() != nil {
			return PreviewResult{}, nil
		}
		return PreviewResult{}, err
	}
	var bands []byte
	if res.PA != nil {
		bands = res.PA.Series.Bytes()
	}
	url, bandsURL, err := a.store.Commit(w, bands)
	if err != nil {
		return PreviewResult{}, err
	}
	if res.PA == nil {
		return PreviewResult{URL: url}, nil
	}
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
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	_, ctx, done := a.jobs.Begin(a.ctx, "previewWindow")
	defer done()
	w, err := a.engine.PreviewWindow(ctx, p, startSec)
	if err != nil {
		if ctx.Err() != nil {
			return WindowResult{}, nil
		}
		return WindowResult{}, err
	}
	url := a.store.Put(audio.WAV16(w.Audio, w.SampleRate))
	if url == "" {
		return WindowResult{}, errors.New("プレビューの一時ファイルを書けません")
	}
	return WindowResult{URL: url, StartSec: w.StartSec, TotalSec: w.TotalSec}, nil
}

// RenderOriginal は曲全体の原音(A/B比較用)のURLを返す。ラウドネスはプレビューと同じ目標にそろえる。
func (a *App) RenderOriginal(p project.Project) (string, error) {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	_, ctx, done := a.jobs.Begin(a.ctx, "original")
	defer done()
	w := a.store.NewWAV()
	if _, err := a.engine.OriginalTo(ctx, p, w); err != nil {
		w.Abort()
		if ctx.Err() != nil {
			return "", nil
		}
		return "", err
	}
	url, _, err := a.store.Commit(w, nil)
	return url, err
}

// StartExport は書き出しジョブを開始してジョブIDを返す。書き出しは一時名のファイルへ書き、成功したときだけ outPath を置き換える
// (失敗・中断では既存のファイルに触らず、一時ファイルも残さない)。
// 進捗は render:progress、完了は render:done、失敗は render:error(中断も render:error)で通知する。
func (a *App) StartExport(p project.Project, outPath string) (string, error) {
	if outPath == "" {
		return "", errors.New("書き出し先が指定されていません")
	}
	id, ctx, done := a.jobs.Begin(a.ctx, "")
	p = p.Clone()
	// 書き出しは設定の変更では中断しない。使い捨てのスプールの置き場所は、開始時の設定で決まる(次の書き出しから新しい設定)
	tempDir := a.currentSettings().CacheDir
	a.exports.Add(1)
	go func() {
		defer a.exports.Done()
		defer done()
		err := render.ExportIn(ctx, tempDir, p, outPath, a.progressEmitter(id))
		switch {
		case errors.Is(err, context.Canceled):
			a.emit("render:error", ErrorEvent{JobID: id, Message: "中断しました"})
		case err != nil:
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

// --- 設定(ディスクキャッシュ) ---

// GetSettings は現在有効な設定を返す。
func (a *App) GetSettings() settings.Settings { return a.currentSettings() }

// SetSettings は設定を検証して保存し、実行中のアプリに反映する。
// 検証(置き場所が絶対パスで、作成でき、書き込めること)か保存に失敗したときは、何も変えずにエラーを返す。
//
// 反映: キャッシュの有効・無効か置き場所が変わるときだけ、プレビューのエンジンを作り直す(古いほうは Close して
// スプールとディレクトリを消す)。置き場所が変わるときは、プレビューのWAVのストアも新しい場所に作り直す
// (再生中のプレビューのURLは404になる。フロントは settings:applied を受けて、プレビューを作り直す)。
// 実行中のプレビュー系のジョブ(曲全体・先行プレビュー・原音)は中断して、終わるのを待ってから差し替える。
// 書き出しのジョブは中断しない(自分専用の一時ディレクトリを使っているので影響されず、次の書き出しから新しい設定になる)。
func (a *App) SetSettings(s settings.Settings) error {
	a.setMu.Lock()
	defer a.setMu.Unlock()
	s, err := settings.Validate(s)
	if err != nil {
		return err
	}
	cur := a.currentSettings()
	if s == cur {
		return nil
	}
	if err := settings.Save(a.settingsPath, s); err != nil {
		return err
	}
	a.mu.Lock()
	a.settings = s
	a.mu.Unlock()
	dirChanged := s.CacheDir != cur.CacheDir
	if dirChanged || s.CacheEnabled != cur.CacheEnabled {
		a.applyCacheSettings(s, dirChanged)
		a.emit("settings:applied", s)
	}
	return nil
}

// applyCacheSettings は、実行中のプレビュー系のジョブを止めて、エンジン(と、置き場所が変わったときはストア)を作り直す。
func (a *App) applyCacheSettings(s settings.Settings, dirChanged bool) {
	// 書き側のロックを待つ間、読み側(プレビュー系のジョブ)を中断し続ける。待ち始めた後は新しい読み側は入れないので、
	// 中断の対象は、すでに走っているジョブだけで、いずれ終わる
	stop, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			for _, kind := range []string{"preview", "previewWindow", "original"} {
				a.jobs.CancelKind(kind)
			}
			select {
			case <-stop:
				return
			case <-t.C:
			}
		}
	}()
	a.cfgMu.Lock()
	// ロックの後は、新しいジョブは始まらない。中断の呼び出しが、ロックを放した後に始まる新しいジョブに当たらないよう、終わりを待つ
	close(stop)
	<-exited
	oldEngine, oldStore := a.engine, a.store
	a.engine = render.NewEngineWith(engineConfig(s))
	if dirChanged {
		a.store = render.NewStoreIn(previewKeep, s.CacheDir)
	}
	a.cfgMu.Unlock()
	oldEngine.Close()
	if dirChanged {
		oldStore.Close()
	}
}

// CacheInfo はキャッシュの現在の置き場所と容量。
type CacheInfo struct {
	// Dir は実際に使っている置き場所(設定が空なら OS の一時フォルダ)。
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	// UsedBytes はキャッシュ(段のスプール)が使っているディスク容量。
	UsedBytes int64 `json:"usedBytes"`
	// FreeBytes は置き場所のボリュームの空き容量。FreeKnown が false(取得できない OS・場所)のときは 0 で、不明。
	FreeBytes int64 `json:"freeBytes"`
	FreeKnown bool  `json:"freeKnown"`
}

// GetCacheInfo は現在のキャッシュの置き場所・使用量・空き容量を返す。
func (a *App) GetCacheInfo() CacheInfo {
	cur := a.currentSettings()
	a.cfgMu.RLock()
	used := a.engine.CacheBytes()
	a.cfgMu.RUnlock()
	info := CacheInfo{Dir: cur.EffectiveDir(), Enabled: cur.CacheEnabled, UsedBytes: used}
	info.FreeBytes, info.FreeKnown = settings.FreeBytes(info.Dir)
	return info
}

// ClearCache は、保持しているキャッシュ(段のスプール・測定値)を今すぐ全部捨てる。
// 再生中のプレビューのWAVは消さない。実行中の処理は、終わるまで自分が使っているファイルを持ち続ける。
func (a *App) ClearCache() error {
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	a.engine.ClearCache()
	return nil
}

// PickCacheDir はキャッシュの置き場所のフォルダを選ぶ。キャンセルは空文字。
func (a *App) PickCacheDir() (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "キャッシュの置き場所を選択", DefaultDirectory: a.currentSettings().EffectiveDir(),
	})
}
