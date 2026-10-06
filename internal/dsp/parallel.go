package dsp

import (
	"runtime"
	"sync"
)

// parallelFor は [0, n) を連続した区間に分け(1区間は minPer 以上、区間数は GOMAXPROCS 以下)、
// 区間ごとに f(lo, hi) を並行に呼んで、全部終わるまで待つ。区間が1つ(n が小さい・GOMAXPROCS が1)なら
// 呼び出し元のゴルーチンで直列に f(0, n) を呼ぶだけ。
//
// f は区間ごとに独立で、状態を共有しないこと(区間の分け方で結果が変わってはいけない)。
// ゴルーチンは呼び出しごとに作って終わる(常駐させない)。
func parallelFor(n, minPer int, f func(lo, hi int)) {
	w := min(runtime.GOMAXPROCS(0), n/max(minPer, 1))
	if w <= 1 {
		f(0, n)
		return
	}
	var wg sync.WaitGroup
	for k := 1; k < w; k++ {
		lo, hi := n*k/w, n*(k+1)/w
		wg.Add(1)
		go func() {
			defer wg.Done()
			f(lo, hi)
		}()
	}
	f(0, n/w)
	wg.Wait()
}
