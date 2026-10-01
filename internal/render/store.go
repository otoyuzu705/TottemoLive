package render

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Store はレンダリング済みのプレビューWAVをメモリに保持し、HTTPで配信する。
// 生PCMをバインディングの戻り値で返すとJSONが巨大になるので、フロントは
// /preview/{id}.wav のURLを <audio> に渡して再生する。Rangeリクエストに対応し、シークできる。
type Store struct {
	mu    sync.Mutex
	seq   int
	items map[string][]byte
	order []string
	keep  int
}

// NewStore は最新 keep 件を保持するストアを返す(古いものから捨てる)。
func NewStore(keep int) *Store {
	return &Store{items: map[string][]byte{}, keep: max(keep, 1)}
}

// Put はWAVを登録し、配信URLのパスを返す。
func (s *Store) Put(wav []byte) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("p%d", s.seq)
	s.items[id] = wav
	s.order = append(s.order, id)
	for len(s.order) > s.keep {
		delete(s.items, s.order[0])
		s.order = s.order[1:]
	}
	return "/preview/" + id + ".wav"
}

// ServeHTTP は /preview/{id}.wav を配信する。それ以外のパスは 404。
func (s *Store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, ok := strings.CutPrefix(r.URL.Path, "/preview/")
	id, isWav := strings.CutSuffix(name, ".wav")
	if !ok || !isWav {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	data, found := s.items[id]
	s.mu.Unlock()
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, id+".wav", time.Time{}, bytes.NewReader(data))
}
