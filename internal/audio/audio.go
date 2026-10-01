// Package audio はffmpeg/ffprobeを子プロセスとして呼び、デコード(生PCM受け取り)とWAVエンコードを行う。
// 形式ごとの純Goデコーダは持たない。ffmpegがPATH上に必要(環境変数 LIVEBIN_FFMPEG / LIVEBIN_FFPROBE で上書き可)。
package audio

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Channels は内部表現のチャンネル数(ステレオ固定)。
const Channels = 2

func bin(env, def string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return def
}

// Available はffmpegが実行できるかを返す。
func Available() bool {
	_, err := exec.LookPath(bin("LIVEBIN_FFMPEG", "ffmpeg"))
	return err == nil
}

// Info は音源ファイルの情報。
type Info struct {
	DurationSec float64 `json:"durationSec"`
	SampleRate  int     `json:"sampleRate"`
	Channels    int     `json:"channels"`
}

// Probe はffprobeで長さ・サンプルレート・チャンネル数を取得する。
func Probe(ctx context.Context, path string) (Info, error) {
	cmd := exec.CommandContext(ctx, bin("LIVEBIN_FFPROBE", "ffprobe"),
		"-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=sample_rate,channels:format=duration",
		"-of", "json", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Info{}, fmt.Errorf("ffprobe %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	var r struct {
		Streams []struct {
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &r); err != nil || len(r.Streams) == 0 {
		return Info{}, fmt.Errorf("ffprobe %s: no audio stream", path)
	}
	sr, _ := strconv.Atoi(r.Streams[0].SampleRate)
	dur, _ := strconv.ParseFloat(r.Format.Duration, 64)
	return Info{DurationSec: dur, SampleRate: sr, Channels: r.Streams[0].Channels}, nil
}

// Decode は音源全体を sr Hz のステレオfloat32にデコードする(リサンプル・チャンネル変換はffmpeg任せ)。
// progress は 0〜1(長さが分かる場合のみ、概算)。
func Decode(ctx context.Context, path string, sr int, progress func(ratio float64)) ([][]float32, error) {
	return decodeRaw(ctx, path, sr, Channels, 0, 0, progress)
}

// DecodeRange は startSec から durSec 秒ぶん(durSec<=0 なら最後まで)をデコードする。
// ファイルの範囲外は返らないので、呼び出し側で長さをそろえること。
func DecodeRange(ctx context.Context, path string, sr int, startSec, durSec float64) ([][]float32, error) {
	return decodeRaw(ctx, path, sr, Channels, startSec, durSec, nil)
}

// peakSampleRate は波形表示用のモノラルデコードのサンプルレート。
const peakSampleRate = 8000

// Peaks は波形表示用のmin/maxピーク列(長さ 2*width、[min0, max0, min1, max1, ...])を返す。
func Peaks(ctx context.Context, path string, width int) ([]float32, error) {
	if width <= 0 {
		return nil, fmt.Errorf("audio: width must be positive")
	}
	buf, err := decodeRaw(ctx, path, peakSampleRate, 1, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	x := buf[0]
	out := make([]float32, 2*width)
	for i := 0; i < width; i++ {
		from, to := len(x)*i/width, len(x)*(i+1)/width
		if to <= from {
			to = min(from+1, len(x))
		}
		lo, hi := float32(0), float32(0)
		for _, v := range x[from:to] {
			lo, hi = min(lo, v), max(hi, v)
		}
		out[2*i], out[2*i+1] = lo, hi
	}
	return out, nil
}

func decodeRaw(ctx context.Context, path string, sr, channels int, startSec, durSec float64, progress func(ratio float64)) ([][]float32, error) {
	var expect float64
	if info, err := Probe(ctx, path); err == nil {
		d := info.DurationSec - startSec
		if durSec > 0 {
			d = math.Min(d, durSec)
		}
		expect = d * float64(sr)
	}
	args := []string{"-v", "error", "-nostdin"}
	if startSec > 0 {
		args = append(args, "-ss", strconv.FormatFloat(startSec, 'f', 6, 64))
	}
	if durSec > 0 {
		args = append(args, "-t", strconv.FormatFloat(durSec, 'f', 6, 64))
	}
	args = append(args, "-i", path, "-vn",
		"-f", "f32le", "-ac", strconv.Itoa(channels), "-ar", strconv.Itoa(sr), "pipe:1")
	cmd := exec.CommandContext(ctx, bin("LIVEBIN_FFMPEG", "ffmpeg"), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg を起動できません: %w", err)
	}

	frameBytes := 4 * channels
	out := make([][]float32, channels)
	chunk := make([]byte, frameBytes*8192)
	have := 0
	for {
		n, rerr := stdout.Read(chunk[have:])
		have += n
		frames := have / frameBytes
		for i := 0; i < frames; i++ {
			for c := 0; c < channels; c++ {
				bits := binary.LittleEndian.Uint32(chunk[i*frameBytes+4*c:])
				out[c] = append(out[c], math.Float32frombits(bits))
			}
		}
		have = copy(chunk, chunk[frames*frameBytes:have])
		if progress != nil && expect > 0 {
			progress(math.Min(float64(len(out[0]))/expect, 1))
		}
		if rerr != nil {
			break
		}
	}
	if err := cmd.Wait(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, fmt.Errorf("ffmpeg decode %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	if len(out[0]) == 0 && startSec <= 0 {
		return nil, fmt.Errorf("ffmpeg decode %s: 音声データがありません", path)
	}
	return out, nil
}

// EncodeWAV はステレオfloat32を24bit WAVとして書き出す(ffmpegへ生PCMをパイプ)。
func EncodeWAV(ctx context.Context, path string, buf [][]float32, sr int, progress func(ratio float64)) error {
	if len(buf) != Channels {
		return fmt.Errorf("audio: stereo buffer required")
	}
	cmd := exec.CommandContext(ctx, bin("LIVEBIN_FFMPEG", "ffmpeg"),
		"-v", "error", "-y", "-f", "f32le", "-ar", strconv.Itoa(sr), "-ac", strconv.Itoa(Channels),
		"-i", "pipe:0", "-c:a", "pcm_s24le", "-f", "wav", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg を起動できません: %w", err)
	}
	werr := writeInterleaved(stdin, buf, progress)
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("ffmpeg encode: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return werr
}

func writeInterleaved(w io.Writer, buf [][]float32, progress func(float64)) error {
	const frames = 8192
	n := len(buf[0])
	b := make([]byte, 0, frames*4*Channels)
	for start := 0; start < n; start += frames {
		b = b[:0]
		for i := start; i < min(start+frames, n); i++ {
			for c := 0; c < Channels; c++ {
				b = binary.LittleEndian.AppendUint32(b, math.Float32bits(buf[c][i]))
			}
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		if progress != nil {
			progress(float64(min(start+frames, n)) / float64(n))
		}
	}
	return nil
}

// WAV16 はステレオfloat32を16bit PCMのWAV(メモリ上)にする。プレビューの再生用。
// 量子化ノイズを散らすためTPDFディザを掛ける(乱数は固定シードで再現可能)。
func WAV16(buf [][]float32, sr int) []byte {
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
