package main

import (
	"path/filepath"
	"testing"
)

func TestCacheFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if s, err := cacheFromEnv(env(nil)); err != nil || !s.CacheEnabled || s.CacheDir != "" {
		t.Errorf("unset: %+v %v", s, err)
	}
	dir := filepath.Join(t.TempDir(), "scratch")
	s, err := cacheFromEnv(env(map[string]string{"TOTTEMOLIVE_CACHE_DIR": dir, "TOTTEMOLIVE_CACHE": "OFF"}))
	if err != nil || s.CacheEnabled || s.CacheDir != dir {
		t.Errorf("set: %+v %v", s, err)
	}
	if _, err := cacheFromEnv(env(map[string]string{"TOTTEMOLIVE_CACHE_DIR": "relative"})); err == nil {
		t.Error("relative dir accepted")
	}
}
