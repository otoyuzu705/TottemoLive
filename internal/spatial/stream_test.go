package spatial

// 参照実装(流し処理にする前の Direct / Sub を一字一句そのまま写したもの)と、流し処理版の一致を確かめる。

import (
	"context"
	"math/rand"
	"testing"

	"tottemolive/internal/dsp"
)

func refDirect(ctx context.Context, in []float32, sr int, dist, azDeg, elDeg float64, set Set, p DirectParams) ([][]float32, error) {
	// 空気吸収(線形位相FIR)の群遅延ぶん、伝搬遅延から引いて、全体の遅れを合わせる(近すぎて引けないぶんは遅れる)
	air := AirFIR(dist, p.AirAbsorption, sr)
	delay := DelaySamples(dist, sr)
	lead := 0
	if air != nil {
		lead = min(delay, AirGroupDelay)
	}
	x := make([]float32, delay-lead+len(in))
	g := float32(Gain(dist, p.Rolloff))
	for i, v := range in {
		x[delay-lead+i] = v * g
	}
	h := set.Lookup(azDeg, elDeg)
	hl, hr := h.L, h.R
	if air != nil {
		// 吸収は、距離ごとの小さなFIRなので、HRIRと先に畳み込んで1回の畳み込みにする(長さは512以下に収まる)
		hl, hr = convolveFIR(h.L, air), convolveFIR(h.R, air)
	}
	earL, earR, err := dsp.ConvolvePair(ctx, x, hl, hr)
	if err != nil {
		return nil, err
	}
	return [][]float32{earL, earR}, nil
}

func refSub(in []float32, sr int, dist float64, extraDelay int, rolloff float64) []float32 {
	delay := DelaySamples(dist, sr) + max(extraDelay, 0)
	g := float32(Gain(dist, rolloff))
	out := make([]float32, delay+len(in))
	for i, v := range in {
		out[delay+i] = v * g
	}
	return out
}

func equalSlices(a, b []float32) (int, bool) {
	if len(a) != len(b) {
		return -1, false
	}
	for i := range a {
		if a[i] != b[i] {
			return i, false
		}
	}
	return 0, true
}

func TestDirectStreamMatchesRef(t *testing.T) {
	const sr = 48000
	set, err := LoadSet("synthetic", sr)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(51))
	ctx := context.Background()
	cases := []struct {
		dist, az, el, air float64
	}{
		{25, 30, 5, 1}, {0.5, -80, 0, 1}, {25, 170, -10, 0}, {80, 0, 20, 0.5}, {3, 90, 0, 1},
	}
	for _, c := range cases {
		for _, n := range []int{0, 1, 1000, 4095, 4096, 4097, 20000} {
			in := make([]float32, n)
			for i := range in {
				in[i] = rng.Float32()*2 - 1
			}
			p := DirectParams{Rolloff: 1, AirAbsorption: c.air}
			want, err := refDirect(ctx, in, sr, c.dist, c.az, c.el, set, p)
			if err != nil {
				t.Fatal(err)
			}
			got, err := Direct(ctx, in, sr, c.dist, c.az, c.el, set, p)
			if err != nil {
				t.Fatal(err)
			}
			for ch := range want {
				if i, ok := equalSlices(got[ch], want[ch]); !ok {
					t.Fatalf("%+v n=%d wrapper ch%d differs at %d (len %d vs %d)", c, n, ch, i, len(got[ch]), len(want[ch]))
				}
			}
			for _, size := range []int{1, 7, 997, 4096, 1 << 20} {
				if size == 1 && n > 5000 {
					continue
				}
				d := NewDirectStream(sr, c.dist, c.az, c.el, set, p)
				var l, r []float32
				for from := 0; from < n; from += size {
					cl, cr := d.Process(in[from:min(from+size, n)])
					l, r = append(l, cl...), append(r, cr...)
				}
				cl, cr := d.Flush()
				l, r = append(l, cl...), append(r, cr...)
				if i, ok := equalSlices(l, want[0]); !ok {
					t.Fatalf("%+v n=%d chunk=%d L differs at %d (len %d vs %d)", c, n, size, i, len(l), len(want[0]))
				}
				if i, ok := equalSlices(r, want[1]); !ok {
					t.Fatalf("%+v n=%d chunk=%d R differs at %d", c, n, size, i)
				}
			}
		}
	}
}

func TestSubStreamMatchesRef(t *testing.T) {
	const sr = 48000
	rng := rand.New(rand.NewSource(52))
	for _, c := range []struct {
		dist  float64
		extra int
	}{{25, 0}, {25, 500}, {0.1, 0}, {40, -5}} {
		for _, n := range []int{0, 1, 1000, 20000} {
			in := make([]float32, n)
			for i := range in {
				in[i] = rng.Float32()*2 - 1
			}
			want := refSub(in, sr, c.dist, c.extra, 1)
			if i, ok := equalSlices(Sub(in, sr, c.dist, c.extra, 1), want); !ok {
				t.Fatalf("%+v n=%d wrapper differs at %d", c, n, i)
			}
			for _, size := range []int{1, 7, 997, 1 << 20} {
				s := NewSubStream(sr, c.dist, c.extra, 1)
				var got []float32
				for from := 0; from < n; from += size {
					got = append(got, s.Process(in[from:min(from+size, n)])...)
				}
				if n == 0 {
					got = append(got, s.Process(nil)...)
				}
				if i, ok := equalSlices(got, want); !ok {
					t.Fatalf("%+v n=%d chunk=%d differs at %d (len %d vs %d)", c, n, size, i, len(got), len(want))
				}
			}
		}
	}
}
