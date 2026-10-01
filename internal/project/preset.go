package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// SoundCrowd は音作りプリセットに含める客席の項目。タイムライン(キーフレーム・手拍子区間)は含めない。
type SoundCrowd struct {
	Density float64 `json:"density"`
	LevelDb float64 `json:"levelDb"`
	SpreadM float64 `json:"spreadM"`
	Seed    int     `json:"seed"`
}

// SoundPreset は音作りプリセット。素材・座席・スピーカー位置・タイムラインを含まないので、
// 別の曲にそのまま適用できる。
type SoundPreset struct {
	PA      PA         `json:"pa"`
	Sub     Sub        `json:"sub"`
	Spatial Spatial    `json:"spatial"`
	Reverb  Reverb     `json:"reverb"`
	Crowd   SoundCrowd `json:"crowd"`
	Output  Output     `json:"output"`
}

// ExtractSoundPreset はプロジェクトから音作りプリセットの部分を取り出す。
func ExtractSoundPreset(p Project) SoundPreset {
	return SoundPreset{
		PA: p.PA, Sub: p.Sub, Spatial: p.Spatial, Reverb: p.Reverb, Output: p.Output,
		Crowd: SoundCrowd{Density: p.Crowd.Density, LevelDb: p.Crowd.LevelDb, SpreadM: p.Crowd.SpreadM, Seed: p.Crowd.Seed},
	}
}

// ApplySoundPreset はプリセットの値を反映したプロジェクトを返す(範囲に丸める)。
func (p Project) ApplySoundPreset(sp SoundPreset) Project {
	p = p.Clone()
	p.PA, p.Sub, p.Spatial, p.Reverb, p.Output = sp.PA, sp.Sub, sp.Spatial, sp.Reverb, sp.Output
	p.Crowd.Density, p.Crowd.LevelDb, p.Crowd.SpreadM, p.Crowd.Seed = sp.Crowd.Density, sp.Crowd.LevelDb, sp.Crowd.SpreadM, sp.Crowd.Seed
	p.Normalize()
	return p
}

// SoundPresetInfo は一覧表示用。ReadOnly は出荷時のプリセット。
type SoundPresetInfo struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"readOnly"`
}

// PresetStore は音作りプリセットの保存先。ユーザープリセットは UserDir/*.json、
// 出荷時のプリセットは Shipped(読み取り専用)。出荷時と同名のユーザープリセットは作れない。
type PresetStore struct {
	UserDir string
	Shipped fs.FS // nil可。ルート直下の *.json
}

const presetExt = ".json"

var ErrPresetReadOnly = errors.New("出荷時のプリセットは変更・削除できません")

// ValidatePresetName はファイル名にできる名前かを検査する。
func ValidatePresetName(name string) error {
	if strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 64 {
		return errors.New("プリセット名は1〜64文字にしてください")
	}
	if strings.ContainsAny(name, "\\/:*?\"<>|") || strings.HasPrefix(name, ".") || strings.TrimSpace(name) != name {
		return errors.New("プリセット名に使えない文字が含まれています(\\ / : * ? \" < > | 、先頭のドット、前後の空白)")
	}
	return nil
}

func (s *PresetStore) List() ([]SoundPresetInfo, error) {
	seen := map[string]bool{}
	var out []SoundPresetInfo
	if entries, err := os.ReadDir(s.UserDir); err == nil {
		for _, e := range entries {
			if n, ok := strings.CutSuffix(e.Name(), presetExt); ok && !e.IsDir() {
				seen[n] = true
				out = append(out, SoundPresetInfo{Name: n})
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if s.Shipped != nil {
		entries, err := fs.ReadDir(s.Shipped, ".")
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if n, ok := strings.CutSuffix(e.Name(), presetExt); ok && !e.IsDir() && !seen[n] {
				out = append(out, SoundPresetInfo{Name: n, ReadOnly: true})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReadOnly != out[j].ReadOnly {
			return out[i].ReadOnly // 出荷時を先に
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *PresetStore) Load(name string) (SoundPreset, error) {
	if err := ValidatePresetName(name); err != nil {
		return SoundPreset{}, err
	}
	data, err := os.ReadFile(filepath.Join(s.UserDir, name+presetExt))
	if errors.Is(err, fs.ErrNotExist) && s.Shipped != nil {
		data, err = fs.ReadFile(s.Shipped, name+presetExt)
	}
	if err != nil {
		return SoundPreset{}, fmt.Errorf("プリセット %q を読めません: %w", name, err)
	}
	// 欠けた項目は既定値、範囲外は丸め(手編集・古いプリセットへの備え)
	base := ExtractSoundPreset(New())
	if err := json.Unmarshal(data, &base); err != nil {
		return SoundPreset{}, fmt.Errorf("プリセット %q: %w", name, err)
	}
	return ExtractSoundPreset(New().ApplySoundPreset(base)), nil
}

// Save はユーザープリセットとして保存する。出荷時と同名は保存できない。
func (s *PresetStore) Save(name string, sp SoundPreset) error {
	if err := ValidatePresetName(name); err != nil {
		return err
	}
	if s.isShipped(name) {
		return ErrPresetReadOnly
	}
	if err := os.MkdirAll(s.UserDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.UserDir, name+presetExt), data, 0o644)
}

// Delete はユーザープリセットを削除する。出荷時のプリセットは削除できない。
func (s *PresetStore) Delete(name string) error {
	if err := ValidatePresetName(name); err != nil {
		return err
	}
	if s.isShipped(name) {
		return ErrPresetReadOnly
	}
	err := os.Remove(filepath.Join(s.UserDir, name+presetExt))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("プリセット %q がありません", name)
	}
	return err
}

func (s *PresetStore) isShipped(name string) bool {
	if s.Shipped == nil {
		return false
	}
	_, err := fs.Stat(s.Shipped, name+presetExt)
	return err == nil
}
