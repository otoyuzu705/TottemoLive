// Package procutil は子プロセス(ffmpeg・ffprobe・demucs)の起動まわりの補助を持つ。
package procutil

import (
	"os/exec"
	"time"
)

// waitDelay は、子プロセスを止めたあと Wait が出力の読み取り(stderr のコピーなど)の終了を待つ上限。
// ffmpeg を shim(Chocolatey や Scoop の ffmpeg.exe は、本物の ffmpeg を子プロセスとして起動して待つ小さな実行ファイル)
// 経由で起動していると、Kill や ctx のキャンセルで止まるのは shim だけで、本物の ffmpeg が stderr のパイプを持ったまま
// 残る。WaitDelay が無いと Wait は、そのパイプが閉じるまで永遠に戻らず、キャンセルや終了の処理が固まる。
const waitDelay = 3 * time.Second

// HideConsole は、コンソールを持たないGUIアプリ(Wailsのリリースビルド)から子プロセスを起動しても、
// コンソールの窓が一瞬出ないようにする。Windows以外では何もしない。
//
// GUIアプリにはコンソールがないので、コンソールアプリを起動するたびにWindowsが新しいコンソール窓
// (既定のターミナルがWindows Terminalなら、そのウィンドウ)を作る。`wails dev` は起動元の
// ターミナルのコンソールを子プロセスが引き継ぐので、開発中は出ず、リリース版でだけ出る。
//
// あわせて cmd.WaitDelay を設定する(未設定のときだけ)。子プロセスはすべてここを通るので、
// 止めたあとの Wait が、孫プロセスの持つパイプのせいで固まることがない。
func HideConsole(cmd *exec.Cmd) {
	hideConsole(cmd)
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = waitDelay
	}
}
