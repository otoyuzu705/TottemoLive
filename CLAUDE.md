# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 現状

M1(エンジンとCLI)とM2(Wails骨格・音作りパネル・区間プレビュー・A/B・書き出し)まで実装済み。M3(会場マップで座席・スピーカー・向きをドラッグ)まで実装済み。M4(客席タイムライン: 歓声キーフレーム・手拍子区間)まで実装済み。M5(ステム分離連携・プリセット追加)は未着手。実装は設計書 `livebin-design.md` に従う。設計と違う実装をする場合は、設計書も同じ変更で更新する。

作るもの: 楽曲音源を「指定した会場・座席で聴いているような」バイノーラル音源に変換して書き出すデスクトップアプリ(アプリ名・Goモジュール名は `livebin`)。Go + Wails v2、フロントは Svelte + TypeScript。

M1時点の暫定: 実測のHRIR・会場IR・客席SEは再配布条件が未確認で同梱していないため、いずれも合成で代用している(HRIRは球形頭部モデル `spatial/synthetic.go`、会場IRは残響時間からの合成 `venue.BuildIR`、客席SEは乱数からの合成 `crowd`)。実素材を同梱するときは `assets/` に置き、`spatial.LoadSet` などを差し替える。

## コマンド

```
go run ./cmd/livebin-cli new -o project.json a.wav b.wav                      # 既定値のプロジェクトを作る
go run ./cmd/livebin-cli render project.json -o out.wav --set pa.lowCutHz=80  # CLI(Wailsなし)。--venue ID で会場切替
go run ./cmd/livebin-cli params     # 音作りパラメーターの一覧
go test ./internal/...              # DSPエンジンのテスト(ffmpegがなければrenderのE2Eはスキップ)
go test ./internal/dsp -run TestXxx # 単一テスト
wails dev                           # GUIの開発起動(Node.js必須)。ブラウザからは http://localhost:34115
wails build                         # 配布ビルド(build/bin/livebin.exe)
cd frontend && npx svelte-check   # フロントの型チェック
```

実行時に `ffmpeg` / `ffprobe` がPATH上に必要(デコード・エンコードを子プロセスで行うため。環境変数 `LIVEBIN_FFMPEG` `LIVEBIN_FFPROBE` で場所を指定可)。Demucsは任意機能。

## 実装順序

M1ではエンジンをCLIで通すところまで(処理の正しさの確認)。音質の追い込みはM2で、UIの音作りパネルと区間プレビューを使って行う。CLIで音質を詰めようとしない。

M1 エンジンとCLI(済) → M2 Wails骨格と音作りパネル・プレビュー(済。音質の追い込みはこれから) → M3 会場マップ(済) → M4 客席タイムライン(済) → M5 ステム分離連携

初版の対象外: リアルタイム処理、ヘッドトラッキング、動画との合成。

## 音作りパラメーター

音に関わる数値はすべてUIから調整できるようにする、というのがこの設計の中心。

- DSPのコードに音に関わる定数を埋めない。Projectのパラメーターとして受け取る
- パラメーターを足すときは、Projectの構造体にフィールドを足し、`internal/params` の `ParamSpec` の表に1行足す。音作りパネルは `ListParams()` から自動生成されるので、フロントにパラメーターごとのコードを書かない
- `ParamSpec.Path`(例 `pa.lowCutHz`)はプロジェクトJSONのパスと一致させる。CLIの `--set` も同じPathを使う
- 値の範囲への丸めはGo側で行う(`LoadProject` とレンダリング開始時)
- リスナー・スピーカー位置は会場マップ、歓声キーフレームと手拍子区間は客席タイムラインで編集し、`ParamSpec` の表には入れない
- 会場プリセットは `venue.speakers` と `reverb.*` を上書きする。音作りプリセットは素材・座席・タイムラインを含まない
- パラメーター一覧と範囲・既定値は設計書の「音作りパラメーター」節が正。範囲と既定値はM2で聴きながら見直す前提の仮の値

## アーキテクチャ上の決まり

**レイヤの境界**

- Wailsに依存してよいのは `main.go` と `app.go` だけ。`internal/` 以下はWailsをimportしない(同じエンジンを `cmd/livebin-cli` から呼ぶため)
- フロントは表示と操作だけ。デコードから書き出しまではすべてGo側の `internal/render` のジョブが実行する
- cgoとPythonをGoのビルドに持ち込まない。FFmpegとDemucsは子プロセスで呼ぶ。Go本体の依存はWailsとgonumに絞る

**信号処理**

- 2系統を最後に1本にまとめる
  - 楽曲: PA質感(EQ・コンプ・歪み)→ 仮想スピーカー(距離減衰・遅延・空気吸収)→ 直接音(HRIR畳み込み)+ 会場残響(会場IR畳み込み)
  - 客席SE: キーフレームの強さカーブ → リスナー周囲に散布(位置ごとにHRIR)
  - ミックス(直接音・残響・客席をそれぞれのレベルで加算)→ マスター(ラウドネス調整 → トゥルーピークリミッタ → WAV)

**プレビュー**

- プレビューは書き出しと同じ処理を区間に限って行う。品質を落とした軽量版の経路を作らない(プレビューで決めた音が書き出しで変わるため)
- `internal/render` は区間プレビューの各段の出力をキャッシュする。各段のキャッシュキーは「その段が読むパラメーターの値 + 上流の段のキー」。段が読むパラメーターを増やしたらキーにも含めること(漏れると値を変えても音が変わらないバグになる)
- 区間の手前を会場IRの長さぶん余分に処理して捨てる
- ラウドネス調整はプレビューでは区間だけの測定になり、書き出しと音量がずれうる(既知の制約)
- 内部表現はfloat32のチャンネル別バッファ、ブロックサイズ1024
- 会場IRは数秒あるので直接畳み込みは使わず、一様分割FFT畳み込み(overlap-save)。FFTは `gonum.org/v1/gonum/dsp/fourier`
- デコードはffmpegから生PCMをパイプで受ける(形式ごとの純Goデコーダは使わない)
- 音源ごとの処理はgoroutineで並列、ミックス段で合流。ジョブは `context.Context` でキャンセル可能にする
- 出力は48kHz / 24bit ステレオWAV

**Wails連携**

- 生PCMや大きな配列をバインディングの戻り値で返さない(JSONが巨大になる)。プレビューはGo側でWAVをメモリに保持し、AssetServerの `Handler` で `/preview/{id}.wav` として配信、フロントは `<audio>` で再生する。シークのためRangeリクエスト対応が必須
- 波形はmin/maxピーク列だけを返し、描画はフロントのcanvas
- 進捗は `runtime.EventsEmit` で通知: `render:progress`(`{jobId, stage, ratio}`、stageは decode / process / encode)、`render:done`、`render:error`
- TS側の型は手書きせず、Wailsのバインディング生成に任せる(Goの構造体が正)。`frontend/wailsjs` は生成物だがコミットする。Goの公開メソッドや構造体を変えたら `wails generate module`(`wails dev`/`wails build` でも自動)で再生成すること
- `App` の公開メソッド一覧は設計書の「Wails連携」節を参照

**データモデル**

- プロジェクトは1つのJSONファイル。音源は絶対パス参照でコピーしない
- 座標はステージ中央を原点としたメートル単位
- 音作りパラメーターは `pa` `spatial` `reverb` `crowd` `output` の各グループに置く
- スキーマは設計書の「データモデル」節を参照。`version` フィールドあり

## 同梱アセットとライセンス

`assets/` に入れるIR・HRIR・客席SEは素材ごとにライセンスが違う。同梱する前に個別に再配布条件を確認し、未確認の素材をコミットしない。

- HRIRはSOFA(HDF5)をGoから直接読まず、事前にWAV + JSONへ変換したものを同梱する
- FFmpegを同梱する場合はLGPLビルドを使い、ライセンス表記を付ける

## git運用

- 基本的にはgitflowに沿う
- コミットメッセージは日本語
- 適切にブランチを切ること
- devにマージする際はPRを立て自己レビューすること