// livebin-cli は同じエンジン(internal/)をWailsなしで呼ぶCLI。動作確認・テスト・一括変換用。
//
//	livebin-cli render project.json -o out.wav --set pa.lowCutHz=80
//	livebin-cli params
//	livebin-cli new -o project.json [音源ファイル ...]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"livebin/internal/params"
	"livebin/internal/project"
	"livebin/internal/render"
	"livebin/internal/venue"
)

const usage = `使い方:
  livebin-cli render <project.json> -o <out.wav> [--set パス=値 ...] [--venue ID]
  livebin-cli params                       音作りパラメーターの一覧
  livebin-cli new -o <project.json> [音源 ...]   既定値のプロジェクトを作る
`

type setFlags []string

func (s *setFlags) String() string     { return strings.Join(*s, ",") }
func (s *setFlags) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "params":
		cmdParams()
	case "new":
		err = cmdNew(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "不明なコマンド: %s\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}

func cmdRender(args []string) error {
	// project.json を先頭の位置引数として受け、残りをフラグとして読む
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return errors.New("プロジェクトファイルを指定してください\n" + usage)
	}
	projPath, rest := args[0], args[1:]
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	out := fs.String("o", "", "出力WAVのパス")
	venueID := fs.String("venue", "", "会場プリセットを切り替える(speakers と reverb.* を上書き)")
	var sets setFlags
	fs.Var(&sets, "set", "パラメーターの上書き。パス=値(複数可)")
	if err := fs.Parse(rest); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-o で出力先を指定してください")
	}

	p, err := project.Load(projPath)
	if err != nil {
		return err
	}
	if *venueID != "" {
		if p, err = venue.Apply(p, *venueID); err != nil {
			return err
		}
	}
	for _, s := range sets {
		path, val, ok := strings.Cut(s, "=")
		if !ok {
			return fmt.Errorf("--set は パス=値 の形式です: %q", s)
		}
		if err := params.SetString(&p, path, val); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	start := time.Now()
	var mu sync.Mutex
	last := ""
	prog := func(stage string, ratio float64) {
		mu.Lock()
		defer mu.Unlock()
		line := fmt.Sprintf("\r%-8s %3.0f%%", stage, ratio*100)
		if line != last {
			fmt.Fprint(os.Stderr, line)
			last = line
		}
	}
	if err := render.Export(ctx, p, *out, prog); err != nil {
		fmt.Fprintln(os.Stderr)
		return err
	}
	fmt.Fprintf(os.Stderr, "\n完了: %s (%.1f秒)\n", *out, time.Since(start).Seconds())
	return nil
}

func cmdParams() {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "パス\t表示名\t範囲\t既定値\t単位")
	for _, s := range params.ListParams() {
		rng, def := fmt.Sprintf("%g〜%g", s.Min, s.Max), fmt.Sprint(s.DefaultValue())
		if s.Kind == params.KindEnum {
			rng = strings.Join(s.Options, "|")
		}
		label := s.Label
		if s.Advanced {
			label += "(詳細)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Path, label, rng, def, s.Unit)
	}
	w.Flush()
}

func cmdNew(args []string) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	out := fs.String("o", "project.json", "出力するプロジェクトファイル")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p := project.New()
	for i, path := range fs.Args() {
		abs, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		p.Sources = append(p.Sources, project.Source{
			ID: fmt.Sprintf("src%d", i+1), Path: abs, Role: project.RoleMix,
		})
	}
	return project.Save(*out, p)
}
