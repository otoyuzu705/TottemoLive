// Package separate はDemucs(Python製、子プロセス)でステム分離を行う。任意機能で、
// Demucsが入っていない環境でもアプリの他の機能は使える。Go本体にPythonは持ち込まない。
//
// 実行するコマンド: demucs --two-stems=vocals -n htdemucs -o <出力先> <音源>
// 出力: <出力先>/htdemucs/<音源名>/vocals.wav と no_vocals.wav
package separate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"tottemolive/internal/procutil"
)

const model = "htdemucs"

// Stems は分離結果のファイル(ボーカルと、ボーカル以外の伴奏)。
type Stems struct {
	Vocals  string `json:"vocals"`
	Backing string `json:"backing"`
}

func bin() string {
	if v := os.Getenv("TOTTEMOLIVE_DEMUCS"); v != "" {
		return v
	}
	return "demucs"
}

// Available はDemucsが実行できるかを返す(環境変数 TOTTEMOLIVE_DEMUCS で場所を指定できる)。
func Available() bool {
	_, err := exec.LookPath(bin())
	return err == nil
}

// cacheKey は音源の同一性(パス・サイズ・更新時刻)とモデルから決まるキー。
func cacheKey(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d|%s", path, fi.Size(), fi.ModTime().UnixNano(), model)))
	return hex.EncodeToString(sum[:8]), nil
}

// Separate は音源をボーカルと伴奏に分離する。結果は cacheDir に保存し、同じ音源の2回目以降は
// 再実行せずに返す。progress は 0〜1(Demucsの進捗表示から読み取る概算)。
func Separate(ctx context.Context, path, cacheDir string, progress func(ratio float64)) (Stems, error) {
	key, err := cacheKey(path)
	if err != nil {
		return Stems{}, err
	}
	final := filepath.Join(cacheDir, key)
	out := Stems{Vocals: filepath.Join(final, "vocals.wav"), Backing: filepath.Join(final, "no_vocals.wav")}
	if fileExists(out.Vocals) && fileExists(out.Backing) {
		if progress != nil {
			progress(1)
		}
		return out, nil
	}
	if !Available() {
		return Stems{}, errors.New("Demucs が見つかりません(PATH に demucs を入れるか、環境変数 TOTTEMOLIVE_DEMUCS で場所を指定してください)")
	}

	tmp := final + ".tmp"
	os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Stems{}, err
	}

	cmd := exec.CommandContext(ctx, bin(), "--two-stems=vocals", "-n", model, "-o", tmp, path)
	procutil.HideConsole(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Stems{}, err
	}
	if err := cmd.Start(); err != nil {
		return Stems{}, fmt.Errorf("demucs を起動できません: %w", err)
	}
	tail := scanProgress(stderr, progress)
	if err := cmd.Wait(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return Stems{}, cerr
		}
		return Stems{}, fmt.Errorf("demucs が失敗しました: %w: %s", err, strings.TrimSpace(tail))
	}

	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	srcDir := filepath.Join(tmp, model, stem)
	if !fileExists(filepath.Join(srcDir, "vocals.wav")) || !fileExists(filepath.Join(srcDir, "no_vocals.wav")) {
		return Stems{}, fmt.Errorf("demucs の出力が見つかりません: %s", srcDir)
	}
	os.RemoveAll(final)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return Stems{}, err
	}
	if err := os.Rename(srcDir, final); err != nil {
		return Stems{}, err
	}
	if progress != nil {
		progress(1)
	}
	return out, nil
}

var percent = regexp.MustCompile(`(\d{1,3})%\|`)

// scanProgress はDemucsの標準エラー(進捗バーはキャリッジリターンで上書きされる)を読み、
// パーセント表示を ratio にして通知する。最後の数行を返す(失敗時のメッセージ用)。
func scanProgress(r io.Reader, progress func(float64)) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var last []string
	best := 0.0
	for sc.Scan() {
		line := sc.Text()
		if m := percent.FindStringSubmatch(line); m != nil {
			if p, err := strconv.Atoi(m[1]); err == nil && progress != nil && float64(p)/100 > best {
				best = float64(p) / 100
				progress(best)
			}
			continue
		}
		if strings.TrimSpace(line) != "" {
			last = append(last, line)
			if len(last) > 5 {
				last = last[1:]
			}
		}
	}
	return strings.Join(last, "\n")
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
