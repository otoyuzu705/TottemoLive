package render

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Store はレンダリング済みのプレビューWAV(と、付随するバイナリ)をメモリに保持し、HTTPで配信する。
// 生PCMや大きな配列をバインディングの戻り値で返すとJSONが巨大になるので、フロントは
// /preview/{id}.wav のURLを <audio> に渡して再生する。Rangeリクエストに対応し、シークできる。
// 付随するバイナリ(PA出力の帯域レベル)は /preview/{id}.bands で配信する。
type Store struct {
	mu    sync.Mutex
	seq   int
	items map[string]item
	order []string
	keep  int
}

type item struct {
	wav   []byte
	bands []byte // nil なら無し
}

// NewStore は最新 keep 件を保持するストアを返す(古いものから捨てる)。
func NewStore(keep int) *Store {
	return &Store{items: map[string]item{}, keep: max(keep, 1)}
}

// Put はWAVを登録し、配信URLのパスを返す。
func (s *Store) Put(wav []byte) string {
	wavPath, _ := s.PutWithBands(wav, nil)
	return wavPath
}

// PutWithBands はWAVと帯域レベルのバイナリ(無ければ nil)を登録し、それぞれの配信URLのパスを返す。
// bands が nil のときの bandsPath は空文字。
func (s *Store) PutWithBands(wav, bands []byte) (wavPath, bandsPath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	s.items[id] = item{wav: wav, bands: bands}
	s.order = append(s.order, id)
	for len(s.order) > s.keep {
		delete(s.items, s.order[0])
		s.order = s.order[1:]
	}
	wavPath = "/preview/" + id + ".wav"
	if bands != nil {
		bandsPath = "/preview/" + id + ".bands"
	}
	return wavPath, bandsPath
}

// ServeHTTP は /preview/{id}.wav と /preview/{id}.bands を配信する。それ以外のパスは 404。
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/preview/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	var id, contentType string
	var pick func(item) []byte
	if base, isWav := strings.CutSuffix(name, ".wav"); isWav {
		id, contentType, pick = base, "audio/wav", func(it item) []byte { return it.wav }
	} else if base, isBands := strings.CutSuffix(name, ".bands"); isBands {
		id, contentType, pick = base, "application/octet-stream", func(it item) []byte { return it.bands }
	} else {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	it, found := s.items[id]
	s.mu.Unlock()
	data := pick(it)
	if !found || data == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}
