// stubdemucs はテスト用の偽Demucs。引数から入力と出力先を読み、
// <出力先>/htdemucs/<音源名>/{vocals,no_vocals}.wav を作る。
// 環境変数 STUB_FAIL=1 で失敗、STUB_SLEEP=1 で長く待つ(中断のテスト用)。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	var out string
	for i, a := range args {
		if a == "-o" && i+1 < len(args) {
			out = args[i+1]
		}
	}
	in := args[len(args)-1]
	fmt.Fprintln(os.Stderr, "Selected model is a bag of 1 models.")
	for _, p := range []int{10, 55, 100} {
		fmt.Fprintf(os.Stderr, "\r %3d%%|#####     | 12/30 [00:01<00:02]", p)
	}
	if os.Getenv("STUB_SLEEP") != "" {
		time.Sleep(30 * time.Second)
	}
	if os.Getenv("STUB_FAIL") != "" {
		fmt.Fprintln(os.Stderr, "\nRuntimeError: CUDA out of memory")
		os.Exit(1)
	}
	data, err := os.ReadFile(in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dir := filepath.Join(out, "htdemucs", strings.TrimSuffix(filepath.Base(in), filepath.Ext(in)))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "vocals.wav"), data, 0o644)
	os.WriteFile(filepath.Join(dir, "no_vocals.wav"), data, 0o644)
}
