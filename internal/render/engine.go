package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
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

	dirMu sync.Mutex
	dir   string // 段の出力(スプール)を置く一時ディレクトリ。最初に要るときに作る
}

// tempDir はスプールを置く一時ディレクトリ(Engine ごと。最初に呼ばれたときに作る)。
func (e *Engine) tempDir() (string, error) {
	e.dirMu.Lock()
	defer e.dirMu.Unlock()
	if e.dir == "" {
		d, err := os.MkdirTemp("", renderTempPrefix+"*")
		if err != nil {
			return "", fmt.Errorf("一時フォルダを作れません: %w", err)
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

// NewEngine はキャッシュ付きのエンジンを返す。
func NewEngine() *Engine { return &Engine{cache: newCache(), analyzePA: true} }

// Preview は曲全体を、書き出しと同じ処理でレンダリングする(段ごとのキャッシュを使う)。
// 音量も曲全体で測るので、書き出しと同じになる。
func (e *Engine) Preview(ctx context.Context, p project.Project, prog Progress) (*Result, error) {
	return e.run(ctx, p, prog)
}

// Original は曲全体の原音(音源にゲインを掛けて足しただけ)を返す。A/B比較用。
// 音量差で判断が偏らないよう、マスター(ラウドネス・リミッタ)だけは通して同じ目標にそろえる。
func (e *Engine) Original(ctx context.Context, p project.Project) (*Result, error) {
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	srcs := e.sources(pp.p.Sources, nil)
	// 音源ごとにデコードして、ゲインを掛けながらバスに足す(全音源を同時には持たない)
	var out [][]float32
	for i, src := range srcs {
		buf, err := src.load(ctx)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = [][]float32{make([]float32, len(buf[0])), make([]float32, len(buf[0]))}
		}
		for c := range out {
			if len(buf[c]) > len(out[c]) {
				out[c] = append(out[c], make([]float32, len(buf[c])-len(out[c]))...)
			}
		}
		g := float32(math.Pow(10, pp.p.Sources[i].GainDb/20))
		for c := range out {
			for k, v := range buf[c] {
				out[c][k] += v * g
			}
		}
	}
	master(out, sampleRate, pp.p.Output)
	return &Result{Audio: out, SampleRate: sampleRate}, nil
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
