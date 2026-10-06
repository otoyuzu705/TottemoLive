package analysis

import (
	"context"
	"math/rand"
	"testing"
)

func TestAnalyzerMatchesRef(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	ctx := context.Background()
	hop := 2400
	lengths := []int{0, 1, 100, 8191, 8192, 8193, hop * 5, hop*7 + 1, hop*9 - 1, 3*hop + 8192, 10 * sr}
	for _, n := range lengths {
		x := make([]float32, n)
		for i := range x {
			x[i] = (rng.Float32()*2 - 1) * 0.5 * float32(1+i%5000) / 5000
		}
		want, err := refCompute(ctx, x, sr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Compute(ctx, x, sr)
		if err != nil {
			t.Fatal(err)
		}
		check := func(name string, s *Series) {
			t.Helper()
			if s.Frames != want.Frames || s.Bands != want.Bands || s.HopSec != want.HopSec || len(s.Data) != len(want.Data) {
				t.Fatalf("n=%d %s: shape %d/%d/%v/%d vs %d/%d/%v/%d", n, name, s.Frames, s.Bands, s.HopSec, len(s.Data), want.Frames, want.Bands, want.HopSec, len(want.Data))
			}
			for i := range want.Data {
				if s.Data[i] != want.Data[i] {
					t.Fatalf("n=%d %s: Data[%d] %v vs %v", n, name, i, s.Data[i], want.Data[i])
				}
			}
		}
		check("Compute", got)
		for _, size := range []int{1000, 2400, 4097, 16384, 1 << 20} {
			if size == 1000 && n > 5*sr {
				continue
			}
			a := NewAnalyzer(sr)
			for from := 0; from < n; from += size {
				if err := a.Write(ctx, x[from:min(from+size, n)]); err != nil {
					t.Fatal(err)
				}
			}
			s, err := a.Finish(ctx)
			if err != nil {
				t.Fatal(err)
			}
			check("chunk "+itoa(size), s)
		}
		// MeanSquareAcc は MeanSquare と同じ(区切りに依らない)
		var acc MeanSquareAcc
		for from := 0; from < n; from += 777 {
			acc.Add(x[from:min(from+777, n)])
		}
		var whole MeanSquareAcc
		whole.Add(x)
		if acc.Value() != whole.Value() || MeanSquare(x) != whole.Value() {
			t.Fatalf("n=%d: mean square %v %v %v", n, acc.Value(), whole.Value(), MeanSquare(x))
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestAnalyzerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := NewAnalyzer(sr)
	if err := a.Write(ctx, make([]float32, 100000)); err == nil {
		t.Error("expected cancellation error")
	}
}
