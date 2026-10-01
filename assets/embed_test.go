package assets

import (
	"testing"

	"livebin/internal/project"
)

// 出荷時プリセットがすべて読め、範囲内の値で、別プロジェクトに適用できる。
func TestShippedPresets(t *testing.T) {
	s := &project.PresetStore{UserDir: t.TempDir(), Shipped: ShippedPresets()}
	list, err := s.List()
	if err != nil || len(list) == 0 {
		t.Fatalf("list: %v %v", list, err)
	}
	for _, info := range list {
		if !info.ReadOnly {
			t.Errorf("%s should be read-only", info.Name)
		}
		sp, err := s.Load(info.Name)
		if err != nil {
			t.Errorf("%s: %v", info.Name, err)
			continue
		}
		// 丸めても変わらない = 最初から範囲内
		again := project.New().ApplySoundPreset(sp)
		if project.ExtractSoundPreset(again) != sp {
			t.Errorf("%s: values out of range", info.Name)
		}
	}
}
