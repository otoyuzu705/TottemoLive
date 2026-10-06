package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// refDecode は、ffmpegの出力を全部読んでから変換する(流し読みにする前のデコードと同じ結果になるはずの参照)。
func refDecode(t *testing.T, path string, sr, channels int) [][]float32 {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-i", path, "-vn",
		"-f", "f32le", "-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(sr), "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	fb := 4 * channels
	n := len(out) / fb
	buf := make([][]float32, channels)
	for c := range buf {
		buf[c] = make([]float32, n)
		for i := 0; i < n; i++ {
			buf[c][i] = math.Float32frombits(binary.LittleEndian.Uint32(out[i*fb+4*c:]))
		}
	}
	return buf
}

func TestDecoderMatchesDecode(t *testing.T) {
	path := makeTone(t, 4)
	ctx := context.Background()
	want := refDecode(t, path, 48000, 2)
	all, err := Decode(ctx, path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	for c := range want {
		if len(all[c]) != len(want[c]) {
			t.Fatalf("Decode length %d vs %d", len(all[c]), len(want[c]))
		}
		for i := range want[c] {
			if all[c][i] != want[c][i] {
				t.Fatalf("Decode differs at ch%d[%d]", c, i)
			}
		}
	}
	for _, size := range []int{1, 7, 1000, 8192, 100000} {
		d, err := OpenDecoder(ctx, path, 48000, nil)
		if err != nil {
			t.Fatal(err)
		}
		if exp := d.ExpectedFrames(); exp < 4*48000-100 || exp > 4*48000+100 {
			t.Errorf("expected frames %d", exp)
		}
		got := [][]float32{nil, nil}
		buf := [][]float32{make([]float32, size), make([]float32, size)}
		reads := 0
		for {
			n, err := d.Read(buf)
			for c := range got {
				got[c] = append(got[c], buf[c][:n]...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if reads++; size == 1 && reads > 20000 { // 細かすぎる読みは、頭の部分だけ照合する
				break
			}
		}
		d.Close()
		n := len(got[0])
		if size != 1 && n != len(want[0]) {
			t.Fatalf("chunk %d: %d frames vs %d", size, n, len(want[0]))
		}
		for c := range want {
			for i := 0; i < n; i++ {
				if got[c][i] != want[c][i] {
					t.Fatalf("chunk %d differs at ch%d[%d]", size, c, i)
				}
			}
		}
	}
}

func TestDecoderErrors(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	ctx := context.Background()
	// 存在しないファイル・音声でないファイルは、Read のエラーになる
	bad := filepath.Join(t.TempDir(), "bad.wav")
	os.WriteFile(bad, []byte("not audio"), 0o644)
	for _, path := range []string{filepath.Join(t.TempDir(), "none.wav"), bad} {
		d, err := OpenDecoder(ctx, path, 48000, nil)
		if err != nil {
			continue
		}
		buf := [][]float32{make([]float32, 100), make([]float32, 100)}
		n, err := d.Read(buf)
		if err == nil || err == io.EOF {
			t.Errorf("%s: read %d frames, err=%v", path, n, err)
		}
		d.Close()
	}
	// キャンセルされたら ctx.Err() が返る
	path := makeTone(t, 4)
	cctx, cancel := context.WithCancel(ctx)
	d, err := OpenDecoder(cctx, path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	buf := [][]float32{make([]float32, 1<<20), make([]float32, 1<<20)}
	var rerr error
	for rerr == nil {
		_, rerr = d.Read(buf)
	}
	if rerr != context.Canceled && rerr != io.EOF {
		t.Errorf("after cancel: %v", rerr)
	}
	d.Close()
	// 途中で閉じても止まる(二重の Close も可)
	d, err = OpenDecoder(ctx, path, 48000, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.Read([][]float32{make([]float32, 10), make([]float32, 10)})
	d.Close()
	d.Close()
}

// refWAV16 は流し書きにする前の WAV16(メモリ上で全部組み立てる版)。
func refWAV16(buf [][]float32, sr int) []byte {
	n := len(buf[0])
	dataLen := n * 2 * Channels
	b := make([]byte, 0, 44+dataLen)
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+dataLen))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1) // PCM
	b = binary.LittleEndian.AppendUint16(b, Channels)
	b = binary.LittleEndian.AppendUint32(b, uint32(sr))
	b = binary.LittleEndian.AppendUint32(b, uint32(sr*2*Channels))
	b = binary.LittleEndian.AppendUint16(b, 2*Channels)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(dataLen))
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < n; i++ {
		for c := 0; c < Channels; c++ {
			v := float64(buf[c][i])*32767 + (rng.Float64() - rng.Float64())
			v = math.Max(-32768, math.Min(32767, math.Round(v)))
			b = binary.LittleEndian.AppendUint16(b, uint16(int16(v)))
		}
	}
	return b
}

func TestWAV16WriterMatchesWAV16(t *testing.T) {
	n := 100000
	buf := [][]float32{make([]float32, n), make([]float32, n)}
	for i := 0; i < n; i++ {
		buf[0][i] = float32(math.Sin(float64(i)*0.01)) * 1.2
		buf[1][i] = float32(math.Cos(float64(i)*0.013)) * 0.4
	}
	want := refWAV16(buf, 48000)
	if got := WAV16(buf, 48000); !bytes.Equal(got, want) {
		t.Fatal("WAV16 differs from the reference")
	}
	for _, size := range []int{1, 777, 65536, n} {
		var out bytes.Buffer
		w, err := NewWAV16Writer(&out, 48000, n)
		if err != nil {
			t.Fatal(err)
		}
		for from := 0; from < n; from += size {
			end := min(from+size, n)
			if err := w.Write([][]float32{buf[0][from:end], buf[1][from:end]}); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), want) {
			t.Fatalf("chunk %d: bytes differ", size)
		}
	}
	// 宣言と違うフレーム数ならエラー
	var out bytes.Buffer
	w, _ := NewWAV16Writer(&out, 48000, 10)
	w.Write([][]float32{make([]float32, 5), make([]float32, 5)})
	if err := w.Close(); err == nil {
		t.Error("frame count mismatch accepted")
	}
}

func TestWAVEncoder(t *testing.T) {
	if !Available() {
		t.Skip("ffmpeg がないためスキップ")
	}
	ctx := context.Background()
	n := 48000 * 2
	buf := [][]float32{make([]float32, n), make([]float32, n)}
	for i := 0; i < n; i++ {
		buf[0][i] = 0.5 * float32(math.Sin(float64(i)*0.05))
		buf[1][i] = 0.25 * float32(math.Sin(float64(i)*0.031))
	}
	path := filepath.Join(t.TempDir(), "out.wav")
	e, err := NewWAVEncoder(ctx, path, 48000)
	if err != nil {
		t.Fatal(err)
	}
	for from := 0; from < n; from += 30000 {
		end := min(from+30000, n)
		if err := e.Write([][]float32{buf[0][from:end], buf[1][from:end]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, path)
	if err != nil || info.SampleRate != 48000 || info.Channels != 2 || info.DurationSec < 1.99 || info.DurationSec > 2.01 {
		t.Fatalf("probe: %+v %v", info, err)
	}
	b, _ := exec.Command("ffprobe", "-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path).Output()
	if s := string(bytes.TrimSpace(b)); s != "pcm_s24le" {
		t.Errorf("codec %q", s)
	}
	back, err := Decode(ctx, path, 48000, nil)
	if err != nil || len(back[0]) != n {
		t.Fatalf("decode back: %v len %d", err, len(back[0]))
	}
	for c := range buf {
		for i := range buf[c] {
			if math.Abs(float64(back[c][i]-buf[c][i])) > 1e-6 {
				t.Fatalf("ch%d[%d]: %v vs %v", c, i, back[c][i], buf[c][i])
			}
		}
	}
	// EncodeWAV(全体を渡す版)も同じファイルになる
	path2 := filepath.Join(t.TempDir(), "out2.wav")
	if err := EncodeWAV(ctx, path2, buf, 48000, nil); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(path)
	c, _ := os.ReadFile(path2)
	if !bytes.Equal(a, c) {
		t.Error("EncodeWAV output differs from WAVEncoder output")
	}
	// Abort は止まる(二重呼び出し・Close 後の呼び出しも可)
	e2, err := NewWAVEncoder(ctx, filepath.Join(t.TempDir(), "abort.wav"), 48000)
	if err != nil {
		t.Fatal(err)
	}
	e2.Write([][]float32{buf[0][:1000], buf[1][:1000]})
	e2.Abort()
	e2.Abort()
	if err := e2.Close(); err != nil {
		t.Errorf("close after abort: %v", err)
	}
}

// 流し読みのPeaksは、従来の(全体を読んでから集計する)計算と、列の境界がブロック単位にずれる分を除いて同じ。
func TestPeaksMatchesFullDecode(t *testing.T) {
	path := makeTone(t, 4)
	ref := refDecode(t, path, peakSampleRate, 1)[0]
	for _, width := range []int{50, 400, 977} {
		got, err := Peaks(context.Background(), path, width)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < width; i++ {
			from, to := len(ref)*i/width, len(ref)*(i+1)/width
			var lo, hi float32
			for _, v := range ref[from:to] {
				lo, hi = min(lo, v), max(hi, v)
			}
			if math.Abs(float64(got[2*i]-lo)) > 0.02 || math.Abs(float64(got[2*i+1]-hi)) > 0.02 {
				t.Fatalf("width %d col %d: got [%v %v], want [%v %v]", width, i, got[2*i], got[2*i+1], lo, hi)
			}
		}
	}
}
