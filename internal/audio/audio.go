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

// Decode は音源を sr Hz のステレオfloat32にデコードする(リサンプル・チャンネル変換はffmpeg任せ)。
// progress は 0〜1(長さが分かる場合のみ、概算)。
func Decode(ctx context.Context, path string, sr int, progress func(ratio float64)) ([][]float32, error) {
	var expect float64
	if info, err := Probe(ctx, path); err == nil {
		expect = info.DurationSec * float64(sr)
	}
	cmd := exec.CommandContext(ctx, bin("LIVEBIN_FFMPEG", "ffmpeg"),
		"-v", "error", "-nostdin", "-i", path, "-vn",
		"-f", "f32le", "-ac", strconv.Itoa(Channels), "-ar", strconv.Itoa(sr), "pipe:1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg を起動できません: %w", err)
	}

	const frameBytes = 4 * Channels
	out := [][]float32{{}, {}}
	chunk := make([]byte, frameBytes*8192)
	have := 0
	for {
		n, rerr := stdout.Read(chunk[have:])
		have += n
		frames := have / frameBytes
		for i := 0; i < frames; i++ {
			for c := 0; c < Channels; c++ {
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
	if len(out[0]) == 0 {
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
