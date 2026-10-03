# TottemoLive

楽曲音源を「会場の指定した座席で聴いているような」バイノーラル音源に変換する、Go＋Wails v2＋Svelte製のデスクトップアプリです。
Windows x64、Intel Mac、Apple Silicon Mac向けの配布ビルドを用意しています。

配布物の導入とFFmpegの設定は[インストール手順](docs/INSTALL.md)を参照してください。
FFmpeg／ffprobeが必須で、Demucsによるステム分離は任意です。

## CI/CD

| ワークフロー | 自動実行 | 手動実行 | 成果物 |
| --- | --- | --- | --- |
| CI（ci.yml） | main／devへのpushとPR | workflow_dispatch | Ubuntuで検証・ビルド。Linux検証用tar.gzとSHA-256 |
| Release Build（release.yml） | GitHub Releasesの公開 | workflow_dispatch | Ubuntuで検証後、Windows／Macの配布ZIPとSHA-256。成果物の保存まで |
| Release desktop build（build.yml） | リリース用ワークフローから呼び出す | 直接実行せずRelease Buildを使用 | Windows／Macの検証・ビルド・ZIP作成 |

通常CIはubuntu-24.04だけで実行します。
Windows x64、Mac Intel、Mac Apple Siliconのネイティブビルドは、Releasesの公開または手動で起動するリリース用ワークフローに限定します。
リリース用ワークフローも最初にUbuntuのCIを実行し、その成功後に各OSのビルドを開始します。
型チェック、フロントエンドのテスト、Goのテストとvet、配布スクリプトのテストに成功した成果物を保存します。
CIにもFFmpeg／ffprobeを導入するため、音声処理のテストを実行できます。
成果物にはFFmpegを同梱しません。

### 手動でCI／ビルドを実行する

ワークフローをGitHubの既定ブランチ（main）に取り込んだ後、次の操作で実行できます。
workflow_dispatchは、ワークフローファイルが既定ブランチに存在する必要があります。

1. GitHubの「Actions」を開きます。
2. 「CI」または「Release Build」を選びます。
3. 「Run workflow」で対象ブランチ／タグを選び、実行します。
4. 成功した実行の「Artifacts」から成果物をダウンロードします。
5. Release Buildの成果物は、artifactのZIPを展開し、中の対象OSの配布ZIPとチェックサムを取り出します。

CIの成果物はTottemoLive-ci-linux-amd64です。Ubuntuでのビルド検証用tar.gzで、Windows／Mac向けの配布物ではありません。
LinuxのGUIビルドにはGTK 3とWebKitGTK 4.1を使用し、Goのテスト・vetとWailsビルドにwebkit2_41タグを付けます。

リリース公開・手動実行ともGitHub Releasesの作成・公開は行いません。
vMAJOR.MINOR.PATCH形式のタグからビルドした場合は、そのタグをアプリの製品バージョンに使用します。
ブランチからの手動ビルドでは、選択したリビジョンのwails.jsonの値を使用します。
成果物の保持期間は14日です。

### 初回リリース前の確認

mainへの取り込み後、最初の実リリースを公開する前に、Release Buildをworkflow_dispatchで一度実行してください。
Windows x64・Intel Mac・Apple Silicon Macの全ビルドと、チェックサム作成まで成功したことを確認します。
失敗した場合はWindowsのFFmpeg導入、Macランナーの選択、Wailsのビルド手順のログを確認し、修正後に再実行してください。
この手動確認ではGitHub Releasesの作成・公開・編集は行いません。

### ご自身でリリースする

1. 公開対象のコミットに、v1.2.3のような`vMAJOR.MINOR.PATCH`形式のタグを付けます。
   タグはGitHubのReleases画面で作成しても、ローカルで作成してpushしても構いません。
   先頭ゼロ、プレリリースやビルドメタデータ付きのタグには対応していません。
2. GitHubのReleases画面でタグを選び、リリースノートを入力して、ご自身で公開します。
3. 公開イベント（release: published）でRelease Buildが開始します。
4. ビルド成功後、ActionsのArtifactsから各OSの配布ZIPとTottemoLive-release-checksums内のSHA256SUMSをダウンロードできます。

CIはビルドとActionsへの成果物保存までを担当します。GitHub Releasesへのファイル添付・編集・公開は行いません。
公開済みリリースの画面からもダウンロードできるようにする場合は、必要なファイルをご自身で添付してください。
タグのpushだけではRelease Buildは起動しません。draftの保存も対象外で、公開時に起動します。
ビルドが失敗しても、すでに公開したリリースの状態は変更しません。
ワークフローの権限はcontents: readだけを使用します。

Windowsのコード署名、AppleのDeveloper ID署名・公証、自動更新は含みません。

## ローカル開発

Goのバージョンはgo.modを参照してください。
Node.js 24、ビルド／配布スクリプトにはPython 3.13を使用します。
GUIビルドには[Wailsの環境構築](https://wails.io/docs/gettingstarted/installation/)とOS別の開発ツールが必要です。
MacはXcode Command Line Toolsが必要です。

Wails CLIはgo.modの依存と同じバージョンにします。

```sh
go install github.com/wailsapp/wails/v2/cmd/wails@v2.16.0
cd frontend
npm ci
npm run check
npm test
npm run build
cd ..
go test ./... -count=1
go vet ./...
python3 -m unittest discover -s scripts -p 'test_*.py' -v
wails dev
```

Windowsでは上記のPythonコマンドを`python`で実行できます。

GUIの配布ビルド：

```sh
wails build -clean -m -nosyncgomod
```

MacのIntel／Apple Siliconを明示する場合は`-platform darwin/amd64`または`-platform darwin/arm64`、Windows x64は`-platform windows/amd64`を指定します。
生成物はbuild/binに出力されます。

配布ZIPの作成例（Apple Silicon Mac）：

```sh
python3 scripts/package.py --target macos-arm64
```

`--target`にはwindows-amd64、macos-amd64、macos-arm64を指定できます。
MacのZIP作成はdittoを使うためMac上で実行してください。
ZIPと.zip.sha256はdistに出力されます。

設計と開発規約は[設計書](TottemoLive-design.md)と[CLAUDE.md](CLAUDE.md)を参照してください。
