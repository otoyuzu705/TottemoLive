// Package assets は同梱アセットを埋め込む。
// IR・HRIR・客席SEは素材ごとに再配布条件を確認してから追加する(未確認の素材はコミットしない)。
package assets

import (
	"embed"
	"io/fs"
)

//go:embed presets/*.json
var presets embed.FS

// ShippedPresets は出荷時の音作りプリセット(ルート直下に *.json)。
func ShippedPresets() fs.FS {
	sub, err := fs.Sub(presets, "presets")
	if err != nil {
		panic(err)
	}
	return sub
}
