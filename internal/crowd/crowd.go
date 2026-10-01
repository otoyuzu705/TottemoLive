// Package crowd は客席SE(歓声・手拍子)を生成し、リスナーの周囲に散布する。
//
// 客席SEの録音素材は再配布条件の確認待ちのため、当面は乱数シードから合成する
// (歓声は帯域制限したノイズのゆらぎ、手拍子は短い減衰ノイズ)。
// 強さカーブ(キーフレーム)は歓声に掛ける。手拍子は clapRanges の区間だけ鳴る。
package crowd

import (
	"context"
	"math"
	"math/rand"
	"sort"
	"sync"

	"livebin/internal/dsp"
	"livebin/internal/project"
	"livebin/internal/spatial"
)

// 客席SEの合成方式の形を決める定数。ユーザーが調整するのは密度・レベル・散布半径・シードと、
// キーフレーム・手拍子区間。
const (
	maxVoices       = 12  // density=1 のときの人数(散布する音源の数)
	minRadiusM      = 1.5 // リスナーに近すぎる配置を避ける
	cheerLowHz      = 300.0
	cheerHighHz     = 3000.0
	clapIntervalSec = 0.5  // 手拍子の平均間隔(120BPM)
	clapJitter      = 0.12 // 間隔のばらつき(割合)
	clapDecaySec    = 0.012
	clapLowHz       = 900.0
	clapHighHz      = 5000.0
	clapGain        = 1.5
)

// Render は長さ n サンプルの客席信号(ステレオ、バイノーラル化済み)を返す。
// レベル(crowd.levelDb)は掛けない(ミックス段で掛ける)。
func Render(ctx context.Context, c project.Crowd, l project.Listener, set spatial.Set, sr, n int) ([][]float32, error) {
	out := [][]float32{make([]float32, n), make([]float32, n)}
	voices := int(math.Ceil(c.Density * maxVoices))
	if voices == 0 || n == 0 {
		return out, nil
	}
	cheerEnv := envelope(c.Keyframes, sr, n)
	hasCheer := cheerEnv != nil
	hasClap := len(c.ClapRanges) > 0
	if !hasCheer && !hasClap {
		return out, nil
	}

	results := make([][][]float32, voices)
	errs := make([]error, voices)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4) // 同時に処理する人数(メモリの上限)
	for v := 0; v < voices; v++ {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[v], errs[v] = renderVoice(ctx, c, l, set, sr, n, v, cheerEnv)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	norm := float32(1 / math.Sqrt(maxVoices))
	for _, r := range results {
		for ch := range out {
			for i := 0; i < n; i++ {
				out[ch][i] += r[ch][i] * norm
			}
		}
	}
	return out, nil
}

// envelope はキーフレームを線形補間した歓声の強さ(サンプルごと)。キーフレームがなければ nil。
func envelope(kf []project.Keyframe, sr, n int) []float32 {
	if len(kf) == 0 {
		return nil
	}
	kf = append([]project.Keyframe(nil), kf...)
	sort.Slice(kf, func(i, j int) bool { return kf[i].T < kf[j].T })
	env := make([]float32, n)
	k := 0
	for i := range env {
		t := float64(i) / float64(sr)
		for k+1 < len(kf) && kf[k+1].T <= t {
			k++
		}
		var v float64
		switch {
		case t <= kf[0].T:
			v = kf[0].Cheer
		case k+1 >= len(kf):
			v = kf[len(kf)-1].Cheer
		default:
			a, b := kf[k], kf[k+1]
			v = a.Cheer + (b.Cheer-a.Cheer)*(t-a.T)/(b.T-a.T)
		}
		env[i] = float32(math.Min(math.Max(v, 0), 1))
	}
	return env
}

func renderVoice(ctx context.Context, c project.Crowd, l project.Listener, set spatial.Set, sr, n, idx int, cheerEnv []float32) ([][]float32, error) {
	rng := rand.New(rand.NewSource(int64(c.Seed)*1_000_003 + int64(idx)*7919 + 1))

	// 位置: リスナー周囲の円盤内(面積一様、minRadiusM〜spreadM)。高さは立っている人の頭の位置
	ang := rng.Float64() * 2 * math.Pi
	r := math.Sqrt(minRadiusM*minRadiusM + rng.Float64()*(c.SpreadM*c.SpreadM-minRadiusM*minRadiusM))
	px, py := l.X+r*math.Cos(ang), l.Y+r*math.Sin(ang)
	az, el, dist := spatial.Direction(l.X, l.Y, l.Z, l.YawDeg, px, py, l.Z)
	// 距離減衰は逆距離則。近い人ほど大きく聞こえる(スピーカーと違い基準距離より近くでも持ち上げる)
	g := float32(math.Min(spatial.RefDistanceM/dist, spatial.MaxGain))

	sig := make([]float32, n)
	if cheerEnv != nil {
		fillCheer(sig, cheerEnv, rng, sr)
	}
	addClaps(sig, c.ClapRanges, rng, sr)
	for i := range sig {
		sig[i] *= g
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	h := set.Lookup(az, el)
	res := make([][]float32, 2)
	for ch, ir := range [][]float32{h.L, h.R} {
		y, err := dsp.Convolve(ctx, sig, ir)
		if err != nil {
			return nil, err
		}
		res[ch] = y[:n]
	}
	return res, nil
}

// fillCheer は歓声(帯域制限ノイズを、ゆっくり揺れる振幅で変調したもの)に強さカーブを掛けて sig に書く。
func fillCheer(sig, env []float32, rng *rand.Rand, sr int) {
	for i := range sig {
		sig[i] = float32(rng.Float64()*2 - 1)
	}
	dsp.HighPass(float64(sr), cheerLowHz).Process(sig)
	dsp.LowPass(float64(sr), cheerHighHz).Process(sig)
	f1, f2 := 0.3+rng.Float64()*1.2, 0.7+rng.Float64()*2
	p1, p2 := rng.Float64()*2*math.Pi, rng.Float64()*2*math.Pi
	for i := range sig {
		t := float64(i) / float64(sr)
		m := 0.65 + 0.2*math.Sin(2*math.Pi*f1*t+p1) + 0.15*math.Sin(2*math.Pi*f2*t+p2)
		sig[i] *= float32(m) * env[i]
	}
}

// addClaps は手拍子区間に、ばらつきのある間隔で短いバーストを足す。
func addClaps(sig []float32, ranges []project.ClapRange, rng *rand.Rand, sr int) {
	n := len(sig)
	burst := make([]float32, int(clapDecaySec*8*float64(sr)))
	for _, rg := range ranges {
		t := rg.Start + rng.Float64()*clapIntervalSec // 人ごとに位相をずらす
		for ; t < rg.End; t += clapIntervalSec * (1 + clapJitter*(rng.Float64()*2-1)) {
			start := int(t * float64(sr))
			if start < 0 || start >= n {
				continue
			}
			for i := range burst {
				d := math.Exp(-float64(i) / (clapDecaySec * float64(sr)))
				burst[i] = float32((rng.Float64()*2 - 1) * d)
			}
			dsp.HighPass(float64(sr), clapLowHz).Process(burst)
			dsp.LowPass(float64(sr), clapHighHz).Process(burst)
			amp := float32(clapGain * (0.7 + 0.3*rng.Float64()))
			for i, v := range burst {
				if start+i >= n {
					break
				}
				sig[start+i] += v * amp
			}
		}
	}
}
