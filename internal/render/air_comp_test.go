package render

import (
	"context"
	"math"
	"reflect"
	"testing"

	"gonum.org/v1/gonum/dsp/fourier"

	"tottemolive/internal/project"
	"tottemolive/internal/spatial"
	"tottemolive/internal/venue"
)

// thirdOctDb は x の 1/3 オクターブ帯域(中心 fcs)のパワー密度(dB)。x を2のべき乗にゼロ詰めしてFFTし、帯域内のbinで平均する。
func thirdOctDb(x []float32, fcs []float64) []float64 {
	N := 1
	for N < len(x) {
		N <<= 1
	}
	buf := make([]float64, N)
	for i, v := range x {
		buf[i] = float64(v)
	}
	X := fourier.NewFFT(N).Coefficients(nil, buf)
	out := make([]float64, len(fcs))
	for i, fc := range fcs {
		lo, hi := int(math.Ceil(fc*math.Pow(2, -1.0/6)*float64(N)/sampleRate)), int(fc*math.Pow(2, 1.0/6)*float64(N)/sampleRate)
		sum, n := 0.0, 0
		for b := lo; b <= hi && b < len(X); b++ {
			sum += real(X[b])*real(X[b]) + imag(X[b])*imag(X[b])
			n++
		}
		out[i] = 10 * math.Log10(sum/float64(n))
	}
	return out
}

// 基準点(FOH)の席では、補正1(上限十分)で空気吸収がほぼ平坦になる(吸収なしと 12.5 kHz まで同じ)。
// 基準点より前の席は、吸収なしより高域が明るくなる。
func TestAirCompFlatAtReference(t *testing.T) {
	fcs := []float64{1000, 2000, 4000, 6300, 8000, 10000, 12500}
	run := func(seatY, absorption, comp float64) []float64 {
		p := project.New()
		p.Venue = project.DefaultVenue()
		p.Sources = []project.Source{{ID: "s", Path: "unused.wav", Role: project.RoleMix}} // 読まない(prepare を通すため)
		p.Sub.Enabled = "off"
		pr, _ := venue.Get(p.Venue.Preset)
		ref := fohReference(pr)
		p.Listener.X, p.Listener.Y, p.Listener.Z = ref[0], seatY, ref[2]
		p.Spatial.AirAbsorption = absorption
		p.Spatial.AirCompensation, p.Spatial.AirCompensationMaxDb = comp, 18
		pp, err := prepare(p)
		if err != nil {
			t.Fatal(err)
		}
		in := [][]float32{make([]float32, 1), make([]float32, 1)}
		in[0][0], in[1][0] = 1, 1
		out, err := directCompute(context.Background(), pp, in, 16384)
		if err != nil {
			t.Fatal(err)
		}
		return thirdOctDb(out[0], fcs)
	}
	pr, _ := venue.Get("arena")
	atRef, none := run(pr.DepthM/2, 1, 1), run(pr.DepthM/2, 0, 0)
	uncomp := run(pr.DepthM/2, 1, 0)
	for i, fc := range fcs {
		t.Logf("%5.0f Hz: reference seat vs no absorption %+.2f dB (uncompensated %+.2f dB)", fc, atRef[i]-none[i], uncomp[i]-none[i])
		if d := atRef[i] - none[i]; math.Abs(d) > 0.3 {
			t.Errorf("reference seat %v Hz: %.2f dB from no absorption", fc, d)
		}
		if fc >= 4000 && none[i]-uncomp[i] < 1 { // 補正なしでは、吸収で実際に落ちている(比較の意味がある)
			t.Errorf("%v Hz: absorption without compensation only drops %.2f dB", fc, none[i]-uncomp[i])
		}
	}
	front, frontNone := run(10, 1, 1), run(10, 0, 0)
	for i, fc := range fcs {
		if fc >= 8000 && front[i] <= frontNone[i] {
			t.Errorf("front seat %v Hz: %.2f dB, should be brighter than no absorption (%.2f)", fc, front[i], frontNone[i])
		}
	}
}

// 補正0では reverbIR は BuildIR そのもの。有効なときは長さが同じで、中域は変わらず、高域が補正量だけ持ち上がる。
func TestReverbIRAirComp(t *testing.T) {
	p := project.New()
	p.Venue = project.DefaultVenue()
	p.Sources = []project.Source{{ID: "s", Path: "unused.wav", Role: project.RoleMix}} // 読まない(prepare を通すため)
	pp, err := prepare(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := venue.BuildIR(pp.pr, pp.p.Reverb, sampleRate)
	ir0, err := reverbIR(ctx, pp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ir0, base) {
		t.Fatal("reverbIR without compensation differs from BuildIR")
	}
	p.Spatial.AirCompensation = 1
	pp, _ = prepare(p)
	ir1, err := reverbIR(ctx, pp)
	if err != nil {
		t.Fatal(err)
	}
	if len(ir1[0]) != len(base[0]) || len(ir1[1]) != len(base[1]) {
		t.Fatalf("length %d vs %d", len(ir1[0]), len(base[0]))
	}
	fcs := []float64{1000, 8000}
	a, b := thirdOctDb(base[0], fcs), thirdOctDb(ir1[0], fcs)
	if d := b[0] - a[0]; math.Abs(d) > 0.3 {
		t.Errorf("1 kHz moved by %.2f dB", d)
	}
	want := radiatedAirComp(pp.p, pp.pr).BoostDb(8000, 1)
	if want < 3 || want > 5 {
		t.Fatalf("unexpected boost %.2f dB at 8 kHz", want)
	}
	t.Logf("reverb IR: 1 kHz %+.2f dB, 8 kHz %+.2f dB (boost %.2f dB)", b[0]-a[0], b[1]-a[1], want)
	if d := b[1] - a[1]; math.Abs(d-want) > 0.7 {
		t.Errorf("8 kHz raised by %.2f dB, want %.2f", d, want)
	}
	// 吸収の倍率が0なら補正も無効
	p.Spatial.AirAbsorption = 0
	pp, _ = prepare(p)
	if c := radiatedAirComp(pp.p, pp.pr); c != (spatial.AirComp{}) {
		t.Errorf("compensation should be off without absorption: %+v", c)
	}
}
