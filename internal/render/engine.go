package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"tottemolive/internal/project"
)

// Engine はプレビューの各段の出力をメモリに保持する。
// 各段のキャッシュキーは「その段が読むパラメーターの値 + 上流の段のキー」で、
// キーが変わった段から下流だけを計算し直す。段が読むパラメーターを増やしたらキーにも入れること
// (漏れると、値を変えても音が変わらないバグになる)。
// 段(スロット)ごとに最新の1件だけを持つので、メモリは曲の長さに比例した一定量で収まる。
type Engine struct {
	cache *cache // nil ならキャッシュなし(書き出し)
	// analyzePA が true のとき、結果にPA出力の帯域レベル(スペクトラム表示用)を含める。プレビュー用だけ。
	analyzePA bool
	decodes   atomic.Int64 // デコードした回数(テスト用)

	// chunk は流し処理の1回のフレーム数(0なら既定の65536。テストでチャンク不変性を確かめるために変える)
	chunk int
	// openSource は音源のデコーダーを開く関数(nil なら audio.OpenDecoder。テストで合成音源を差し込む)
	openSource func(ctx context.Context, path string, sr int, progress func(float64)) (frameReader, error)

	// baseDir は一時ディレクトリを作る場所(空なら OS の一時ディレクトリ)。キャッシュの置き場所の設定
	baseDir string

	dirMu sync.Mutex
	dir   string // 段の出力(スプール)を置く一時ディレクトリ(baseDir の下)。最初に要るときに作る
}

// defaultChunk は流し処理の1回のフレーム数。
const defaultChunk = 65536

func (e *Engine) chunkSize() int {
	if e.chunk > 0 {
		return e.chunk
	}
	return defaultChunk
}

// tempDir はスプールを置く一時ディレクトリ(Engine ごと。最初に呼ばれたときに作る)。
func (e *Engine) tempDir() (string, error) {
	e.dirMu.Lock()
	defer e.dirMu.Unlock()
	if e.dir == "" {
		d, err := mkTempDir(e.baseDir, renderTempPrefix)
		if err != nil {
			return "", err
		}
		e.dir = d
	}
	return e.dir, nil
}

// Close はキャッシュの出力(スプール)を捨て、一時ディレクトリを消す。アプリの終了時に呼ぶ。
func (e *Engine) Close() error {
	if e.cache != nil {
		e.cache.closeAll()
	}
	e.dirMu.Lock()
	defer e.dirMu.Unlock()
	if e.dir == "" {
		return nil
	}
	err := os.RemoveAll(e.dir)
	e.dir = ""
	return err
}

// NewEngine はキャッシュ付きのエンジンを返す(一時ファイルは OS の一時ディレクトリ)。
func NewEngine() *Engine { return NewEngineWith(EngineConfig{CacheEnabled: true}) }

// EngineConfig はプレビュー用のエンジンの設定(アプリの設定から決まる)。
type EngineConfig struct {
	// CacheEnabled が false なら、段の出力もメモリ上の測定値(inputLevel・paSpectrum)も一切保持せず、
	// プレビューは毎回全段を計算し直す。処理に必須の使い捨てのスプール(ミックス・先行プレビューのPA段)は作るが、
	// 処理が終わると消える。
	CacheEnabled bool
	// Dir は一時ディレクトリを作る場所。空なら OS の一時ディレクトリ。
	Dir string
}

// NewEngineWith は設定に従うプレビュー用のエンジンを返す(PA出力の帯域レベルの解析は常に行う)。
func NewEngineWith(cfg EngineConfig) *Engine {
	e := &Engine{analyzePA: true, baseDir: cfg.Dir}
	if cfg.CacheEnabled {
		e.cache = newCache()
	}
	return e
}

// mkTempDir は base(空なら OS の一時ディレクトリ)の下に、prefix で始まる一時ディレクトリを作る。
// base が無ければ作る(設定した置き場所が、後から消されていても動くように)。
func mkTempDir(base, prefix string) (string, error) {
	if base != "" {
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", fmt.Errorf("一時フォルダを作れません: %w", err)
		}
	}
	d, err := os.MkdirTemp(base, prefix+"*")
	if err != nil {
		return "", fmt.Errorf("一時フォルダを作れません: %w", err)
	}
	return d, nil
}

// CacheEnabled はキャッシュを使うエンジンか。
func (e *Engine) CacheEnabled() bool { return e.cache != nil }

// ClearCache は保持している段の出力(スプール)と測定値をすべて捨てる(使用中の実行は、自分の参照が
// 残っているので、終わるまでファイルは消えない)。
func (e *Engine) ClearCache() {
	if e.cache != nil {
		e.cache.closeAll()
	}
}

// CacheBytes はキャッシュが使っているディスク容量(段のスプールの合計バイト数)。
func (e *Engine) CacheBytes() int64 {
	if e.cache == nil {
		return 0
	}
	return e.cache.spoolBytes()
}

// Preview は曲全体を、書き出しと同じ処理でレンダリングする(段ごとのキャッシュを使う)。
// 音量も曲全体で測るので、書き出しと同じになる。結果の音声をメモリに持つので、テストと短い素材用。
// 長い曲は PreviewTo で、出力をファイルへ流す。
func (e *Engine) Preview(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	return e.renderMem(ctx, p, prog)
}

// PreviewTo は曲全体を、書き出しと同じ処理でレンダリングして sink へ流す(段ごとのキャッシュを使う)。
func (e *Engine) PreviewTo(ctx context.Context, p project.Project, prog Progress, sink Sink) (*Result, error) {
	return e.RenderTo(ctx, p, prog, sink)
}

// CacheStat は段(スロット)ごとの計算回数とキャッシュヒット回数。
type CacheStat struct{ Computed, Hits int }

// Stats はスロットごとの統計を返す(テストと診断用)。
func (e *Engine) Stats() map[string]CacheStat {
	if e.cache == nil {
		return nil
	}
	return e.cache.stats()
}

type cacheEntry struct {
	key string
	val any
}

type cache struct {
	mu    sync.Mutex
	slots map[string]cacheEntry
	stat  map[string]CacheStat
}

func newCache() *cache {
	return &cache{slots: map[string]cacheEntry{}, stat: map[string]CacheStat{}}
}

func (c *cache) stats() map[string]CacheStat {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]CacheStat, len(c.stat))
	for k, v := range c.stat {
		out[k] = v
	}
	return out
}

// memo はスロット slot の出力を返す。キャッシュのキーが key と一致すれば再計算しない。
// 計算が失敗・キャンセルされたときは何も保存しない。c が nil なら常に計算する。
func memo[T any](c *cache, slot, key string, compute func() (T, error)) (T, error) {
	if c != nil {
		c.mu.Lock()
		if e, ok := c.slots[slot]; ok && e.key == key {
			s := c.stat[slot]
			s.Hits++
			c.stat[slot] = s
			c.mu.Unlock()
			return e.val.(T), nil
		}
		// 失敗・中断のときに備えて古い出力を残す価値は小さい(キーが変わった段は、どのみち作り直す)ので、
		// 作り直す前に捨てて、新旧が同時にメモリに載らないようにする
		delete(c.slots, slot)
		c.mu.Unlock()
	}
	v, err := compute()
	if err != nil {
		var zero T
		return zero, err
	}
	if c != nil {
		c.mu.Lock()
		c.slots[slot] = cacheEntry{key: key, val: v}
		s := c.stat[slot]
		s.Computed++
		c.stat[slot] = s
		c.mu.Unlock()
	}
	return v, nil
}

// lookup はスロット slot の値(メモリに持つもの)が key と一致すれば返す。一致しなければ、古いエントリを外す。
// c が nil なら常に false。
func (c *cache) lookup(slot, key string) (any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.slots[slot]; ok {
		if e.key == key {
			s := c.stat[slot]
			s.Hits++
			c.stat[slot] = s
			return e.val, true
		}
		c.dropLocked(slot)
	}
	return nil, false
}

// put は新しく計算した値(メモリに持つもの)をスロットに登録する。
func (c *cache) put(slot, key string, v any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropLocked(slot)
	c.slots[slot] = cacheEntry{key: key, val: v}
	s := c.stat[slot]
	s.Computed++
	c.stat[slot] = s
}

// hashKey は値の並びからキャッシュキーを作る。構造体はフィールド名つきで展開されるので、
// フィールドを足したときにキーへ自動的に反映される。
func hashKey(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%#v|", p)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// fileIdentity はファイルの同一性(パス・サイズ・更新時刻)。中身が差し替わったらキーが変わる。
func fileIdentity(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return path + "|missing"
	}
	return fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())
}
