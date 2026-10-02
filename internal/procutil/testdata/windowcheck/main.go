// windowcheck は、コンソールを持たないGUIアプリ(-H=windowsgui)から audio.Decode を数回呼ぶだけの確認用プログラム。
// check.ps1 と組み合わせて、ffmpeg/ffprobe の起動でコンソール窓が出ないことを確かめる。
//
//	go build -ldflags "-H=windowsgui" -o windowcheck.exe ./internal/procutil/testdata/windowcheck
//	powershell -File internal/procutil/testdata/windowcheck/check.ps1 -Exe .\windowcheck.exe -Audio 曲.wav
package main

import (
	"context"
	"os"

	"tottemolive/internal/audio"
)

func main() {
	path := os.Args[1]
	for i := 0; i < 3; i++ {
		// ffprobe は結果の再利用で最初の1回だけ、ffmpeg は毎回起動する
		if _, err := audio.Decode(context.Background(), path, 48000, nil); err != nil {
			os.Exit(1)
		}
	}
}
