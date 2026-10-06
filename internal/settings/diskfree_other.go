//go:build !windows && !linux && !darwin

package settings

// FreeBytes は、この OS では空き容量を取得しない(常に ok=false)。
func FreeBytes(dir string) (free int64, ok bool) { return 0, false }
