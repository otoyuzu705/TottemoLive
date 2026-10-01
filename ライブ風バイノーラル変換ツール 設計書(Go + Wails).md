# ライブ風バイノーラル変換ツール 設計書(Go + Wails)

Oct 2, 2026 · @otoyuzu

## 概要とゴール

楽曲音源を読み込み、指定した会場と座席で聴いているようなバイノーラル音源を書き出すデスクトップアプリ。信号処理と書き出しはGoで行い、UIはWails上のフロントエンド(Svelte + TypeScript想定)で組む。

- 入力: WAV / FLAC / MP3。ボーカルと伴奏のステム推奨、2mixのみも可
- 処理: PA質感付け → 仮想スピーカー配置 → 会場残響 → 客席ノイズ → バイノーラル化
- 出力: 48kHz / 24bit ステレオWAV(ヘッドホン再生前提)
- 初版の対象外: リアルタイム処理、ヘッドトラッキング、動画との合成

## 全体アーキテクチャ

フロントは表示と操作だけを担当し、デコードから書き出しまでの処理はすべてGo側のrenderジョブが実行する。

&#91;embedded content: 全体構成 · フロント・Go・外部プロセス\]

FFmpegとDemucsは子プロセスとして呼ぶので、GoのビルドにcgoやPythonを持ち込まずに済む。

## 信号処理パイプライン

楽曲は「PA → 仮想スピーカー → 直接音と残響」、客席SEは「強さカーブ → 周囲に散布」の2系統で処理し、最後に1本にまとめる。

&#91;embedded content: 信号処理パイプライン · 楽曲系統と客席系統\]

- PA質感: 低域カット、高域を少し丸めるシェルフ、軽いコンプと歪みで「スピーカー越し」の音にする
- 仮想スピーカー: 左右のメインPAを置き、リスナーまでの距離から遅延・減衰・高域の空気吸収を計算する
- 直接音: スピーカーごとに方向に合うHRIRを畳み込む
- 会場残響: 会場IRを畳み込み、直接音と混ぜる量は`reverbMix`で決める
- 客席SE: 複数の素材をリスナー周囲のランダムな位置に置き、キーフレームの強さで音量を動かす
- マスター: ラウドネスを目標値に合わせ、トゥルーピークリミッタで仕上げる

## Goバックエンド パッケージ構成

DSPは`internal/`以下に役割ごとに分け、Wailsに依存するのは`main.go`と`app.go`だけにする。こうしておけば同じエンジンをCLIからも呼べる。

```
livebin/
├── main.go          // Wails起動、AssetServerハンドラ登録
├── app.go           // フロントに公開するApp構造体
├── cmd/livebin-cli/ // M1用のCLI(Wailsなし)
├── internal/
│   ├── audio/       // デコード・エンコード(ffmpeg子プロセス)、リサンプル
│   ├── dsp/         // 分割FFT畳み込み、Biquad、コンプ、リミッタ
│   ├── spatial/     // HRIR読み込み、仮想音源、距離モデル
│   ├── venue/       // 会場プリセット、IR管理
│   ├── crowd/       // 客席SEのスケジューラ
│   ├── render/      // 処理グラフ組み立て、ジョブ実行、進捗通知
│   ├── project/     // プロジェクトファイルの読み書き
│   └── separate/    // ステム分離(任意、外部プロセス)
├── assets/          // 同梱IR・HRIR・客席SE
└── frontend/        // Svelte + TS
```

実装上の要点:

- 内部表現はfloat32のチャンネル別バッファ。ブロックサイズ1024で処理する
- 会場IRは数秒あるため、直接畳み込みではなく一様分割FFT畳み込み(overlap-save)を使う。FFTは`gonum.org/v1/gonum/dsp/fourier`
- デコードはffmpegを子プロセスで呼び、生PCMをパイプで受ける。形式ごとの純Goデコーダの差を吸収するため
- 音源ごとの処理はgoroutineで並列に回し、ミックス段で合流させる
- ジョブは`context.Context`でキャンセル可能にする

## Wails連携(バインディング・イベント・プレビュー)

重い処理はすべてGo側に置き、フロントにはメソッド呼び出し・進捗イベント・プレビュー音声のURLだけを渡す。Wails v2(安定版)を前提にし、着手時点でv3が正式版になっていれば移行を検討する。

**App構造体のバインディング**

| メソッド | 役割 |
| --- | --- |
| `OpenAudioFiles() ([]SourceInfo, error)` | ネイティブのファイル選択、長さ・サンプルレート取得 |
| `GetPeaks(sourceID string, width int) ([]float32, error)` | 波形表示用のmin/maxピーク列 |
| `ListVenues() []VenuePreset` | 会場プリセット一覧 |
| `LoadProject(path string) (Project, error)` / `SaveProject(path string, p Project) error` | プロジェクトの読み書き |
| `RenderPreview(p Project, startSec, lenSec float64) (string, error)` | 指定区間を軽い設定でレンダリングし、プレビューURLを返す |
| `StartExport(p Project, outPath string) (string, error)` | 書き出しジョブを開始し、ジョブIDを返す |
| `CancelJob(jobID string)` | ジョブの中断 |

**イベント(`runtime.EventsEmit`)**

- `render:progress` … `{jobId, stage, ratio}`。stageは decode / process / encode
- `render:done` … `{jobId, path}`
- `render:error` … `{jobId, message}`

**プレビュー音声の受け渡し**

- 生PCMをバインディングで返すとJSONが巨大になるので使わない
- Go側でレンダリングしたWAVをメモリに保持し、AssetServerの`Handler`で`/preview/{id}.wav`として配信する
- フロントは`<audio>`で再生する。Rangeリクエストに対応させてシークできるようにする
- 波形もピーク列だけを返し、描画はフロントのcanvasで行う

## フロントエンドUI設計

1画面構成で、左に素材と会場、中央に会場マップ、下に波形と客席タイムラインを置く。Projectはフロントのstoreで持ち、変更から500ms待って区間プレビューを作り直す。

1. 素材パネル: ドラッグ&ドロップで投入。トラックごとに役割(ボーカル / 伴奏 / 2mix)とゲインを設定
2. 会場パネル: プリセット選択(ライブハウス / ホール / アリーナ / ドーム)と残響量
3. 会場マップ: 上から見た図にステージ・PAスピーカー・リスナーを表示。リスナー(座席)をドラッグで動かす
4. 客席タイムライン: 波形の上に歓声の強さのキーフレームを打つ。手拍子区間、曲前後の歓声を配置
5. トランスポート: プレビュー区間の再生と、原音とのA/B切り替え
6. 書き出しダイアログ: 出力形式、ラウドネス目標、進捗バー、中断ボタン

## データモデル(プロジェクトファイル)

プロジェクトは1つのJSONファイルに保存し、音源は絶対パスで参照する(コピーしない)。同じ構造体をGoとTSで共有し、TS側の型はWailsのバインディング生成に任せる。座標はステージ中央を原点としたメートル単位。

```json
{
  "version": 1,
  "sources": [
    { "id": "vo", "path": "/path/vocal.wav", "role": "vocal", "gainDb": 0 },
    { "id": "inst", "path": "/path/inst.wav", "role": "backing", "gainDb": -1.5 }
  ],
  "venue": {
    "preset": "arena",
    "reverbMix": 0.35,
    "speakers": [
      { "id": "L", "x": -12, "y": 0, "z": 8 },
      { "id": "R", "x": 12, "y": 0, "z": 8 }
    ]
  },
  "listener": { "x": 0, "y": 25, "z": 1.2, "yawDeg": 0 },
  "pa": { "lowCutHz": 60, "highShelfDb": -3, "compRatio": 3, "drive": 0.2 },
  "crowd": {
    "density": 0.7,
    "keyframes": [
      { "t": 0.0, "cheer": 0.9 },
      { "t": 6.0, "cheer": 0.1 }
    ],
    "clapRanges": [{ "start": 52.0, "end": 80.0 }]
  },
  "output": { "sampleRate": 48000, "bitDepth": 24, "targetLufs": -14 }
}
```

## 外部依存・素材とライセンス

Go本体の依存はWailsとgonumに絞り、重いものは子プロセスとデータファイルとして外に置く。素材はそれぞれライセンスが違うので、同梱前に個別に確認する。

| 項目 | 候補 | 注意点 |
| --- | --- | --- |
| デスクトップ枠 | Wails | ビルドにNode.jsが必要 |
| FFT | gonum `dsp/fourier` | 純Go、cgo不要 |
| デコード / エンコード | FFmpeg(子プロセス) | 同梱するならLGPLビルドとライセンス表記 |
| HRIR | MIT KEMAR、SADIE IIなど | SOFA形式はHDF5でGoから読みにくい。事前にPythonでWAV + JSONへ変換して同梱 |
| 会場IR | OpenAIRなど | IRごとにライセンス条件が異なる |
| 客席SE | フリー素材、自前収録 | 再配布の可否を確認 |
| ステム分離 | Demucs(Python、子プロセス) | 重くGPU推奨。初版では任意機能 |

変換した音源を公開する場合は、原曲側の利用条件に従う。piaproで配布されているインストも、その配布条件の範囲で使う。

## マイルストーン

最初に音の品質をCLIで詰めてから、UIを載せる。UIを先に作ると、音作りの試行錯誤のたびに画面側も直すことになるため。

1. M1 CLI版: 固定プリセットで「ステム → PA → HRIR → 会場IR → 書き出し」を通す。ここで音質を詰める
2. M2 Wails骨格: ファイル投入、会場プリセット選択、書き出し、進捗表示
3. M3 会場マップとプレビュー: 座席ドラッグ、区間プレビュー、A/B比較
4. M4 客席タイムライン: 歓声キーフレーム、手拍子区間
5. M5 ステム分離連携とプリセット追加
