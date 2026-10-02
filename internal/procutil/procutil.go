// Package procutil は子プロセス(ffmpeg・ffprobe・demucs)の起動まわりの補助を持つ。
package procutil

import "os/exec"

// HideConsole は、コンソールを持たないGUIアプリ(Wailsのリリースビルド)から子プロセスを起動しても、
// コンソールの窓が一瞬出ないようにする。Windows以外では何もしない。
//
// GUIアプリにはコンソールがないので、コンソールアプリを起動するたびにWindowsが新しいコンソール窓
// (既定のターミナルがWindows Terminalなら、そのウィンドウ)を作る。`wails dev` は起動元の
// ターミナルのコンソールを子プロセスが引き継ぐので、開発中は出ず、リリース版でだけ出る。
func HideConsole(cmd *exec.Cmd) {
	hideConsole(cmd)
}
