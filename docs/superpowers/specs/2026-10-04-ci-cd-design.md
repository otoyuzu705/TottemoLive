# Windows／Mac向けCI/CD設計

## 目的と合意した範囲

TottemoLiveの変更を自動検証し、WindowsとMacで利用できるデスクトップアプリをGitHub Releasesから配布する。
ユーザーは、CIでのテストとビルド、バージョンタグによる配布、利用手順の整備に同意した。
後続の合意により、通常CIはUbuntuを使い、Windows／Macのネイティブビルドはリリース用ワークフローだけで実行する。
FFmpeg／ffprobeは同梱せず、利用者が別途インストールする。
Demucsは現在と同じ任意機能とする。
追加要望として、CIと配布用ビルドの両方をworkflow_dispatchで手動実行できるようにする。

## 構成の選択

GitHub Actionsの通常CIはUbuntuで検証とLinuxのGUIビルドを行う。
リリース用ワークフローは、Ubuntuでの検証に成功した後、各OSのネイティブビルドを行う。
既存リポジトリがGitHubにあり、WailsのGUIを各OS上で検証できるため、この構成を採用する。
クロスコンパイルだけではMacのGUIビルドを十分に検証できない。
外部CIサービスを追加する必要はない。

## CI

- PR、対象ブランチへのpush、手動実行で検証する。対象ブランチは実装時にリポジトリの既定ブランチとgitflow運用を確認して決める。
- 通常CIはubuntu-24.04だけで実行する。LinuxビルドのGTK 3とWebKitGTK 4.1をインストールし、Goの検証とWailsビルドにwebkit2_41タグを付ける。
- Goのバージョンはgo.mod、Wails CLIのバージョンは同ファイルのWails依存に合わせる。
- Node.jsはフロントエンドのテストとViteが動作する固定メジャーを選択する。
- npm ciでロックファイルからインストールし、型チェック、フロントエンドのテスト、ビルドを実行する。
- FFmpeg／ffprobeをCIにインストールし、存在を確認してGoの音声処理テストがスキップされないようにする。
- Goのテストと静的検査、WailsのLinux本番ビルドを実行する。
- Linuxの成果物は検証用tar.gzとして保存し、Windows／Mac向けの配布物とは区別する。
- 成果物をActionsからダウンロードできるようにする。

## CDと成果物

- vで始まるバージョンタグのpushをリリースの起点とする。
- リリース用ワークフローだけでWindows x64、Mac Intel、Mac Apple Siliconをビルドする。
- タグ・手動実行とも、まずUbuntuのCIを再利用して検証し、成功後にOS別ビルドを開始する。
- 配布用ビルドにもworkflow_dispatchを設定する。手動実行では選択したブランチ／タグから全対象をビルドし、成果物とチェックサムをActionsのartifactとして保存する。手動実行によるGitHub Releasesへの公開は行わない。
- CIと同じ検証を通過したビルドだけを配布する。
- WindowsはTottemoLive.exeを含むZIP、Macは.appを含むアーキテクチャ別ZIPとして配布する。
- ZIPには利用手順を添付し、成果物のSHA-256チェックサムを公開する。
- 全対象のビルドが成功したらSHA256SUMSをartifactとして保存する。タグ付けとGitHub Releasesの作成・添付・公開はユーザーが手作業で行う。
- 全ジョブを読み取り権限とし、CIはGitHub Releasesを作成・公開しない。
- アプリのバージョン表記をタグに合わせる。追跡対象の設定ファイルはCI内の変更をコミットしない。
- GitHubへのpush、タグ作成、実際の公開は今回のローカル実装に含めない。

## 実行時の依存と利用手順

WindowsではWebView2 RuntimeとFFmpeg／ffprobe、MacではFFmpeg／ffprobeの準備方法を説明する。
MacのFinder起動はシェルとPATHが異なるため、既存の環境変数による指定も含め、実際にGUIから音声を処理できる起動手順を提供する。
Demucsの導入とTOTTEMOLIVE_DEMUCSの指定は任意の手順として記載する。
署名証明書やAppleの公証用資格情報は提供されていないため、初回は未署名で配布する。
OSによる初回起動時の確認について利用手順に記載する。
自動アップデート、FFmpeg同梱、インストーラー、署名・公証は今回の範囲外とする。

## 変更箇所

GitHub Actionsのワークフロー、必要なパッケージ作成スクリプト、READMEと配布用の利用手順を追加する。
再現可能なインストールのため、Wailsのfrontend:installをnpm ciに変更する。
音声処理やUIの機能は変更しない。

## 検証と完了条件

ワークフローの構文と利用する公式Actions／Wailsのオプションを確認する。
ローカルでGoとフロントエンドの検証、MacのGUIビルド、パッケージ内容の確認を行う。
利用可能な環境で実行できない検証は結果として明示する。
Windowsのネイティブ実行とGitHub上のCI/CD成功は、ローカルで確認したと主張しない。
設定と手順の追加、実行可能な検証の成功を実装の完了条件とする。
