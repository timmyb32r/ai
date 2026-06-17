// Command cli is a thin demo CLI that wraps the chineseasr library.
// It is not the product; it demonstrates usage and allows quick manual testing.
//
// Usage:
//
//	go run ./examples/cli \
//	    -ffmpeg   $(which ffmpeg) \
//	    -sherpa   /path/to/sherpa-onnx-offline \
//	    -model-dir /path/to/model \
//	    audio.mp3
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/timmyb32r/001_omc_cri/internal/chineseasr"
)

func main() {
	ffmpegPath := flag.String("ffmpeg", "", "path to ffmpeg binary (required)")
	sherpaPath := flag.String("sherpa", "", "path to sherpa-onnx-offline binary (required)")
	modelDir := flag.String("model-dir", "", "directory containing model file(s) + tokens.txt (required)")
	model := flag.String("model", "sense-voice", "ASR model: sense-voice | paraformer | whisper")
	lang := flag.String("lang", "zh", "source language hint (zh/yue/en/ja/ko/auto)")
	punct := flag.Bool("punct", true, "emit punctuation (maps to --sense-voice-use-itn=1 for SenseVoice); when omitted, the library default (ON) applies")
	threads := flag.Int("threads", 2, "number of inference threads")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] <audio-file>\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}
	audioPath := flag.Arg(0)

	if *ffmpegPath == "" || *sherpaPath == "" || *modelDir == "" {
		fmt.Fprintln(os.Stderr, "error: -ffmpeg, -sherpa, and -model-dir are all required")
		flag.Usage()
		os.Exit(1)
	}

	// Only forward Punctuation when the user explicitly passed -punct; otherwise
	// leave it nil so the library default (ON) applies. flag.Visit reports only
	// the flags that were actually set on the command line.
	var punctuation *bool
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "punct" {
			punctuation = punct
		}
	})

	cfg := chineseasr.Config{
		FFmpegPath:        *ffmpegPath,
		SherpaOfflinePath: *sherpaPath,
		ModelDir:          *modelDir,
		Model:             chineseasr.Model(*model),
		Language:          *lang,
		Punctuation:       punctuation,
		NumThreads:        *threads,
	}

	t, err := chineseasr.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to initialize transcriber: %v\n", err)
		os.Exit(1)
	}

	result, err := t.Transcribe(context.Background(), audioPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: transcription failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result.Text)
}
