package analysis

import (
	"context"
	"math"
	"testing"
)

const sr = 48000

func sine(freq, amp float64, n int) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/sr))
	}
	return x
}

func bandIndex(fc float64) int {
	for i, c := range BandCenters {
		if c == fc {
			return i
		}
	}
	return -1
}

func frame(s *Series, f int) []float32 { return s.Data[f*s.Bands : (f+1)*s.Bands] }

// 0 dBFS の正弦波は、その帯域が約 0 dB(補正の検証)。半分の振幅で約 -6 dB。離れた帯域は十分に小さい。
func TestSineCalibration(t *testing.T) {
	for _, f := range []float64{1000, 1010, 997, 100, 8000} {
		s, err := Compute(context.Background(), sine(f, 1, 2*sr), sr)
		if err != nil {
			t.Fatal(err)
		}
		mid := frame(s, s.Frames/2)
		// f を含む帯域(中心 fc、f ∈ [fc/2^(1/6), fc·2^(1/6)))
		for i, fc := range BandCenters {
			lo, hi := bandEdges(fc)
			if f >= lo && f < hi {
				if math.Abs(float64(mid[i])) > 1.0 {
					t.Errorf("%v Hz: band %v Hz = %.2f dB, want ~0", f, fc, mid[i])
				}
			} else if math.Abs(math.Log2(fc/f)) > 1.5 && mid[i] > -50 {
				t.Errorf("%v Hz: band %v Hz leaks %.1f dB", f, fc, mid[i])
			}
		}
	}
	s, _ := Compute(context.Background(), sine(1000, 0.5, 2*sr), sr)
	if got := frame(s, s.Frames/2)[bandIndex(1000)]; math.Abs(float64(got)+6.02) > 1.0 {
		t.Errorf("half amplitude: %.2f dB", got)
	}
}

func TestFramesAndHop(t *testing.T) {
	s, err := Compute(context.Background(), sine(440, 0.5, sr), sr)
	if err != nil {
		t.Fatal(err)
	}
	if s.Frames != 20 || s.Bands != 31 || len(s.Data) != 20*31 || math.Abs(s.HopSec-0.05) > 1e-9 {
		t.Errorf("frames=%d bands=%d len=%d hop=%v", s.Frames, s.Bands, len(s.Data), s.HopSec)
	}
	empty, err := Compute(context.Background(), nil, sr)
	if err != nil || empty.Frames != 0 || len(empty.Data) != 0 {
		t.Errorf("empty input: %+v %v", empty, err)
	}
}

func TestSilenceIsFloor(t *testing.T) {
	s, _ := Compute(context.Background(), make([]float32, sr), sr)
	for i, v := range s.Data {
		if v != FloorDb {
			t.Fatalf("data[%d]=%v, want floor", i, v)
		}
	}
}

// 低域の狭い帯域(31.5 Hz帯は約7 Hz幅)でも値が出る。
func TestLowBand(t *testing.T) {
	s, _ := Compute(context.Background(), sine(31.5, 1, 2*sr), sr)
	if got := frame(s, s.Frames/2)[bandIndex(31.5)]; math.Abs(float64(got)) > 1.5 {
		t.Errorf("31.5 Hz band = %.2f dB", got)
	}
}

// 後半だけ鳴る音は、前のフレームが無音で、後ろのフレームに現れる(時間の位置が合っている)。
func TestTimeLocalization(t *testing.T) {
	x := make([]float32, 4*sr)
	copy(x[2*sr:], sine(1000, 0.8, 2*sr))
	s, _ := Compute(context.Background(), x, sr)
	i := bandIndex(1000)
	early, late := frame(s, int(0.5/s.HopSec))[i], frame(s, int(3.0/s.HopSec))[i]
	if early > -80 || late < -5 {
		t.Errorf("early %.1f dB, late %.1f dB", early, late)
	}
	// 鳴り始め(2.0秒)の前後で、半分のフレーム(窓の中心がずれる)で立ち上がる
	before, after := frame(s, int(1.6/s.HopSec))[i], frame(s, int(2.4/s.HopSec))[i]
	if before > -60 || after < -10 {
		t.Errorf("around the onset: before %.1f dB, after %.1f dB", before, after)
	}
}

func TestComputeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Compute(ctx, make([]float32, 20*sr), sr); err == nil {
		t.Error("expected cancellation error")
	}
}

func TestMonoAndMeanSquare(t *testing.T) {
	m := Mono([]float32{1, 0, 0.5, 9}, []float32{0, 1, 0.5})
	if len(m) != 3 || m[0] != 0.5 || m[1] != 0.5 || m[2] != 0.5 {
		t.Errorf("mono %v", m)
	}
	if got := MeanSquare([]float32{3, 4}); math.Abs(got-12.5) > 1e-9 {
		t.Errorf("mean square %v", got)
	}
	if MeanSquare(nil) != 0 {
		t.Error("empty mean square")
	}
}
