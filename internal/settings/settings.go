// Package settings はアプリの設定(ディスクキャッシュを使うか・置き場所)の保存と検証を行う。
// 音作りパラメーターではないので、internal/params の ParamSpec には入れない。Wails には依存しない。
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Version は設定ファイルの形式のバージョン。
const Version = 1

// Settings はアプリの設定。JSON でユーザー設定フォルダに保存する。
type Settings struct {
	Version int `json:"version"`
	// CacheEnabled が true なら、プレビューの段の出力(PA・直接音・残響)をディスクに置いて再利用する。
	// false なら何も保持せず、プレビューは毎回全段を計算し直す(処理に必須の使い捨ての一時ファイルは作る)。
	CacheEnabled bool `json:"cacheEnabled"`
	// CacheDir は一時ファイル(キャッシュ・使い捨てのスプール・プレビューのWAV)を置くフォルダ。
	// 空なら OS の一時フォルダ。空でなければ絶対パス。
	CacheDir string `json:"cacheDir"`
}

// Default は既定の設定。
func Default() Settings { return Settings{Version: Version, CacheEnabled: true} }

// Load は path の設定を読む。ファイルが無い・壊れている・読めないときは既定値を返す(起動を止めない)。
// 一部の項目だけ無いファイルは、無い項目を既定値で補う。
func Load(path string) Settings {
	s := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return Default()
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Default()
	}
	s.Version = Version
	return s
}

// Save は設定を path へ保存する(一時ファイルへ書いてから置き換える。途中で失敗しても元のファイルは壊れない)。
func Save(path string, s Settings) error {
	s.Version = Version
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("設定のフォルダを作れません: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "settings-*.tmp")
	if err != nil {
		return fmt.Errorf("設定を保存できません: %w", err)
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("設定を保存できません: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("設定を保存できません: %w", err)
	}
	return nil
}

// Validate は設定を検証して、整えた設定(パスは Clean 済み)を返す。
// CacheDir が空でなければ、絶対パスであること、無ければ作成できること、書き込めること(試しに一時ファイルを作って消す)を確かめる。
// 失敗したときは、原因の分かる日本語のエラーを返す。
func Validate(s Settings) (Settings, error) {
	s.Version = Version
	if s.CacheDir == "" {
		return s, nil
	}
	if !filepath.IsAbs(s.CacheDir) {
		return s, fmt.Errorf("キャッシュの置き場所は絶対パスで指定してください: %q", s.CacheDir)
	}
	s.CacheDir = filepath.Clean(s.CacheDir)
	if fi, err := os.Stat(s.CacheDir); err == nil && !fi.IsDir() {
		return s, fmt.Errorf("キャッシュの置き場所がフォルダではありません: %s", s.CacheDir)
	}
	if err := os.MkdirAll(s.CacheDir, 0o755); err != nil {
		return s, fmt.Errorf("キャッシュの置き場所を作れません(%s): %w", s.CacheDir, err)
	}
	f, err := os.CreateTemp(s.CacheDir, "tottemolive-write-test-*")
	if err != nil {
		return s, fmt.Errorf("キャッシュの置き場所に書き込めません(%s): %w", s.CacheDir, err)
	}
	name := f.Name()
	f.Close()
	if err := os.Remove(name); err != nil {
		return s, fmt.Errorf("キャッシュの置き場所のテストファイルを消せません(%s): %w", s.CacheDir, err)
	}
	return s, nil
}

// EffectiveDir は実際に使うフォルダ。CacheDir が空なら OS の一時フォルダ。
func (s Settings) EffectiveDir() string {
	if s.CacheDir == "" {
		return os.TempDir()
	}
	return s.CacheDir
}
