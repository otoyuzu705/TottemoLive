# TottemoLiveのインストール

楽曲音源を会場・座席に応じたバイノーラル音源に変換するデスクトップアプリです。
Go、Node.js、Pythonは、配布されたアプリを使うだけなら不要です。
ステム分離を使う場合だけ、別途DemucsとそのPython環境が必要です。

## 配布物の選択

| ファイル | 対象 |
| --- | --- |
| TottemoLive-windows-amd64.zip | Windows 10／11、Intel／AMDの64bit PC |
| TottemoLive-macos-amd64.zip | Intel Mac |
| TottemoLive-macos-arm64.zip | Apple Silicon Mac（M1以降） |

GitHub ReleasesのAssetsから対象OSのZIPを取得します。公開後にCIがビルドして自動添付するため、配布ZIPが揃うまではRelease Buildの完了をお待ちください。
手動ビルドの成果物はGitHub Actionsの「Release Build」の成功した実行のArtifactsから取得できます。
Actionsのartifact自体もZIPなので、まず外側を展開し、中のTottemoLive-*.zipも展開してください。
OSの実際の対応範囲は利用するWailsとGoに依存します。リリース用ワークフローではWindows Server 2022とmacOS 15で検証します。通常CIのLinux成果物はビルド検証用です。

## Windows

1. 配布ZIPをフォルダーに展開します。
2. [MicrosoftのWebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/)をインストールします。導入済みなら不要です。
3. FFmpegをインストールし、ffmpeg.exeとffprobe.exeのあるフォルダーをPATHに追加します。
   Chocolateyが導入済みなら、管理者のPowerShellで次のコマンドを実行できます。

   ```powershell
   choco install ffmpeg --yes
   ```

4. 新しいPowerShellを開いて、次のコマンドが両方成功することを確認します。

   ```powershell
   ffmpeg -version
   ffprobe -version
   ```

5. 展開したTottemoLive.exeを起動します。

PATHを変更せず起動する場合は、実際の保存先に合わせて環境変数を設定し、そのPowerShellから起動してください。

```powershell
$env:TOTTEMOLIVE_FFMPEG = 'C:\Tools\ffmpeg\bin\ffmpeg.exe'
$env:TOTTEMOLIVE_FFPROBE = 'C:\Tools\ffmpeg\bin\ffprobe.exe'
& 'C:\Apps\TottemoLive\TottemoLive.exe'
```

配布物は未署名です。SmartScreenの確認が出た場合は、取得元とチェックサムを確認してください。
組織のポリシーで未署名アプリが禁止されている場合は、管理者に相談してください。

## Mac

1. 配布ZIPを展開し、TottemoLive.appを「アプリケーション」に移動します。
2. [Homebrew](https://brew.sh/)を導入し、ターミナルでFFmpegをインストールします。

   ```sh
   brew install ffmpeg
   ffmpeg -version
   ffprobe -version
   ```

3. Finderから起動するとシェルのPATHが引き継がれずFFmpegを検出できないことがあります。
   FFmpeg／ffprobeの絶対パスを渡すため、次のコマンドでアプリを起動してください。
   フォルダーを変更した場合はアプリのパスも変更します。

   ```sh
   TOTTEMOLIVE_FFMPEG="$(command -v ffmpeg)" \
   TOTTEMOLIVE_FFPROBE="$(command -v ffprobe)" \
   /Applications/TottemoLive.app/Contents/MacOS/TottemoLive
   ```

アプリは未署名で、Appleによる公証も行っていません。
初回にmacOSが起動をブロックした場合は、取得元を確認したうえで「システム設定」→「プライバシーとセキュリティ」の「このまま開く」を使用します。
その後、上記のターミナルから再度起動してください。

## Demucs（任意のステム分離）

通常の音源の読み込み・変換・書き出しにはDemucsは不要です。
ステム分離を使う場合は、[Demucsの導入手順](https://github.com/facebookresearch/demucs)に従って対応するPython環境へインストールします。
初回にはモデルのダウンロードが発生します。

demucsがPATHにない場合は、TOTTEMOLIVE_DEMUCSに実行ファイルの絶対パスを設定し、同じターミナル／PowerShellからアプリを起動します。
Macの例（FFmpegの指定も併用してください）：

```sh
export TOTTEMOLIVE_DEMUCS='/実際の仮想環境/bin/demucs'
```

Windowsの例：

```powershell
$env:TOTTEMOLIVE_DEMUCS = 'C:\実際の仮想環境\Scripts\demucs.exe'
```

## チェックサム

ReleasesにはSHA256SUMS、Actionsのartifactには各ZIPの.zip.sha256と、TottemoLive-release-checksums内のSHA256SUMSを保存します。
ZIPを展開する前にSHA-256を比較してください。

Windows：

```powershell
Get-FileHash .\TottemoLive-windows-amd64.zip -Algorithm SHA256
```

Mac（Apple Siliconの例）：

```sh
shasum -a 256 TottemoLive-macos-arm64.zip
```

表示されたハッシュを、チェックサムファイルの該当するZIPの行と比較します。
