//go:build linux || darwin

package settings

import "syscall"

// FreeBytes は dir があるボリュームの、一般ユーザーが使える空き容量(バイト)。取得できなければ ok=false。
func FreeBytes(dir string) (free int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize)), true
}
