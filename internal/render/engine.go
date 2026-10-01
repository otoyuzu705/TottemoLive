package render

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sync"

	"livebin/internal/params"
	"livebin/internal/project"
	"livebin/internal/venue"
)

// 区間プレビューの長さの上限(秒)。
const MaxPreviewSec = 120.0

// Engine は区間プレビューの各段の出力をメモリに保持する。
// 各段のキャッシュキーは「その段が読むパラメーターの値 + 上流の段のキー」で、
// キーが変わった段から下流だけを計算し直す。段が読むパラメーターを増やしたらキーにも入れること
// (漏れると、値を変えても音が変わらないバグになる)。
// 段(スロット)ごとに最新の1件だけを持つので、メモリは区間の長さに比例した一定量で収まる。
type Engine struct {
	cache *cache // nil ならキャッシュなし(書き出し)
}

// NewEngine はキャッシュ付きのエンジンを返す。
func NewEngine() *Engine { return &Engine{cache: newCache()} }

// Preview は曲の startSec 秒から lenSec 秒ぶんを、書き出しと同じ処理でレンダリングする。
// 区間の手前は会場IRの長さぶん余分に処理して捨てる。
func (e *Engine) Preview(ctx context.Context, p project.Project, startSec, lenSec float64, prog Progress) (*Result, error) {
	w, err := previewWindow(p, startSec, lenSec)
	if err != nil {
		return nil, err
	}
	return e.run(ctx, p, w, prog)
}

// Original は同じ区間の原音(音源にゲインを掛けて足しただけ)を返す。A/B比較用。
// 音量差で判断が偏らないよう、マスター(ラウドネス・リミッタ)だけは通して同じ目標にそろえる。
func (e *Engine) Original(ctx context.Context, p project.Project, startSec, lenSec float64) (*Result, error) {
	w, err := previewWindow(p, startSec, lenSec)
	if err != nil {
		return nil, err
	}
	pp, err := prepare(p)
	if err != nil {
		return nil, err
	}
	srcs, err := e.decodeStage(ctx, pp.p.Sources, w, nil)
	if err != nil {
		return nil, err
	}
	// デコード結果はキャッシュと共有なので、ゲインを掛けながら新しいバッファに足す
	out := [][]float32{make([]float32, w.n), make([]float32, w.n)}
	for i, s := range srcs {
		g := float32(math.Pow(10, pp.p.Sources[i].GainDb/20))
		for c := range out {
			for k := range out[c] {
				out[c][k] += s.bufs[c][w.pre+k] * g
			}
		}
	}
	master(out, sampleRate, pp.p.Output)
	return &Result{Audio: out, SampleRate: sampleRate}, nil
}

func previewWindow(p project.Project, startSec, lenSec float64) (window, error) {
	if lenSec <= 0 || lenSec > MaxPreviewSec {
		return window{}, fmt.Errorf("render: プレビュー長は 0 より大きく %g 秒以下にしてください", MaxPreviewSec)
	}
	if startSec < 0 {
		startSec = 0
	}
	pr, ok := venue.Get(p.Venue.Preset)
	if !ok {
		return window{}, fmt.Errorf("render: 不明な会場 %q", p.Venue.Preset)
	}
	return window{
		start: int(math.Round(startSec * sampleRate)),
		pre:   int(math.Ceil(maxTailSec(pr) * sampleRate)),
		n:     int(math.Round(lenSec * sampleRate)),
	}, nil
}

// maxTailSec は、残響パラメーターを範囲の上限まで振っても会場IRが収まる長さ。
// 手前の余分をこの長さに固定するので、残響パラメーターを動かしてもデコード・PAのキャッシュが生きる。
func maxTailSec(pr venue.Preset) float64 {
	pre, _ := params.Find("reverb.preDelayMs")
	decay, _ := params.Find("reverb.decayScale")
	return venue.IRSeconds(pr, project.Reverb{PreDelayMs: pre.Max, DecayScale: decay.Max})
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
