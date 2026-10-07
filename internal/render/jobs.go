package render

import (
	"context"
	"fmt"
	"sync"
)

// Jobs は実行中のジョブをIDで管理し、中断できるようにする。
// 同じ種類(kind)のジョブは同時に1つだけで、新しく始めると古いほうを中断する
// (パラメーターが変わったら、古い値でのレンダリングを捨てて最新の値でやり直すため)。
type Jobs struct {
	mu      sync.Mutex
	seq     int
	cancels map[string]context.CancelFunc
	byKind  map[string]string
}

func NewJobs() *Jobs {
	return &Jobs{cancels: map[string]context.CancelFunc{}, byKind: map[string]string{}}
}

// Begin はジョブを登録し、ID・中断可能なコンテキスト・終了通知の関数を返す。
// kind が空でなければ、同じ kind の実行中ジョブを中断する。終了時は必ず done を呼ぶこと。
func (j *Jobs) Begin(parent context.Context, kind string) (id string, ctx context.Context, done func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	id = fmt.Sprintf("%s-%d", orDefault(kind, "job"), j.seq)
	ctx, cancel := context.WithCancel(parent)
	if kind != "" {
		if old, ok := j.byKind[kind]; ok {
			if c := j.cancels[old]; c != nil {
				c()
			}
		}
		j.byKind[kind] = id
	}
	j.cancels[id] = cancel
	return id, ctx, func() {
		j.mu.Lock()
		defer j.mu.Unlock()
		cancel()
		delete(j.cancels, id)
		if kind != "" && j.byKind[kind] == id {
			delete(j.byKind, kind)
		}
	}
}

// CancelKind は、実行中の kind のジョブを中断する。無ければ何もしない。
func (j *Jobs) CancelKind(kind string) {
	j.mu.Lock()
	id, ok := j.byKind[kind]
	c := j.cancels[id]
	j.mu.Unlock()
	if ok && c != nil {
		c()
	}
}

// Cancel はジョブを中断する。存在しないIDは無視する。
func (j *Jobs) Cancel(id string) {
	j.mu.Lock()
	c := j.cancels[id]
	j.mu.Unlock()
	if c != nil {
		c()
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// CancelAll は実行中のジョブをすべて中断する(アプリの終了時)。
func (j *Jobs) CancelAll() {
	j.mu.Lock()
	cs := make([]context.CancelFunc, 0, len(j.cancels))
	for _, c := range j.cancels {
		cs = append(cs, c)
	}
	j.mu.Unlock()
	for _, c := range cs {
		c()
	}
}
