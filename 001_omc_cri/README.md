# chineseasr

A Go library for offline transcription of Chinese-speech audio files to Simplified-Chinese text with punctuation. It drives two external binaries as subprocesses: `ffmpeg` (decodes and resamples the input to 16 kHz mono WAV) and `sherpa-onnx-offline` (runs ASR; default model SenseVoice-Small). All tool and model paths are caller-provided via `Config`; the library performs no network I/O at runtime.

---

## What each Config path points to

| Field | Points to |
|---|---|
| `FFmpegPath` | The `ffmpeg` binary (e.g. `/usr/local/bin/ffmpeg`). |
| `SherpaOfflinePath` | The `sherpa-onnx-offline` executable from a sherpa-onnx release. |
| `ModelDir` | A directory containing the model's weight file(s) **and** `tokens.txt` — for SenseVoice-Small this is `model.int8.onnx` + `tokens.txt`. |

---

## Required tools

### ffmpeg
Any recent version. Used to decode arbitrary audio/video containers and resample to 16 kHz mono PCM WAV.

### sherpa-onnx-offline
Download a prebuilt release from the pinned URL:

- Release page: https://github.com/k2-fsa/sherpa-onnx/releases
- The binary to use is `sherpa-onnx-offline` (or `sherpa-onnx-offline.exe` on Windows). Pick the asset matching your OS/arch.
- Alternatively, `pip install sherpa-onnx` installs the binary into your Python environment's `bin/` directory.

### SenseVoice-Small model (default)
- Package name (k2-fsa model hub): **`sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17`**
- Download tarball from: https://github.com/k2-fsa/sherpa-onnx/releases
- After extraction the directory contains `model.int8.onnx` and `tokens.txt`; pass that directory as `ModelDir`.

---

## Installation

### macOS

```bash
# 1. Install ffmpeg
brew install ffmpeg

# 2. Install sherpa-onnx-offline (choose one)
#    a) pip (installs sherpa-onnx-offline into the active Python env)
pip install sherpa-onnx
#    b) prebuilt release binary
#       Download from https://github.com/k2-fsa/sherpa-onnx/releases
#       (asset: sherpa-onnx-*-osx-*.tar.bz2) and copy the binary to $PATH.

# 3. Download and extract the SenseVoice-Small model
#    Download sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2
#    from https://github.com/k2-fsa/sherpa-onnx/releases, then:
tar xf sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2
# The extracted directory contains model.int8.onnx + tokens.txt — use its path as ModelDir.
```

### Linux

```bash
# 1. Install ffmpeg
apt-get install -y ffmpeg

# 2. Install sherpa-onnx-offline (choose one)
#    a) pip
pip install sherpa-onnx
#    b) prebuilt release binary
#       Download from https://github.com/k2-fsa/sherpa-onnx/releases
#       (asset: sherpa-onnx-*-linux-*.tar.bz2) and copy the binary to $PATH.

# 3. Download and extract the SenseVoice-Small model
tar xf sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17.tar.bz2
# The extracted directory contains model.int8.onnx + tokens.txt — use its path as ModelDir.
```

---

## Usage

### Go library

```go
import "github.com/timmyb32r/001_omc_cri"

cfg := chineseasr.Config{
    FFmpegPath:        "/usr/local/bin/ffmpeg",
    SherpaOfflinePath: "/usr/local/bin/sherpa-onnx-offline",
    ModelDir:          "/path/to/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17",
    Model:             chineseasr.ModelSenseVoice, // default; also ModelParaformer, ModelWhisper
    Language:          "zh",                       // zh / yue / en / ja / ko / auto
    NumThreads:        2,
}

// Punctuation defaults ON when Punctuation is nil.
// To force it on or off explicitly, pass a pointer:
p := true
cfg.Punctuation = &p // explicitly on
// cfg.Punctuation = nil means "use the default" (also on)

t, err := chineseasr.New(cfg)
if err != nil {
    log.Fatal(err)
}

result, err := t.Transcribe(context.Background(), "audio.mp3")
if err != nil {
    log.Fatal(err)
}
fmt.Println(result.Text) // Simplified Chinese with punctuation
```

### CLI example

```bash
go run ./examples/cli \
    -ffmpeg   $(which ffmpeg) \
    -sherpa   /path/to/sherpa-onnx-offline \
    -model-dir /path/to/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-2024-07-17 \
    audio.mp3
```

All flags: `-ffmpeg`, `-sherpa`, `-model-dir`, `-model` (default `sense-voice`), `-lang` (default `zh`), `-punct` (bool, default `true`), `-threads` (int, default `2`). The single positional argument is the audio file path.

---

## Offline scope

The library performs **no network I/O**: the `net` package is never imported, and ffmpeg is invoked with `-protocol_whitelist file,pipe` so it cannot fetch remote URLs. Remote audio inputs (any path containing `://`) are rejected with `ErrRemoteInput` before any subprocess runs.

This offline guarantee covers the **Go layer and the locally-resolved binaries and model** — the caller is responsible for supplying binaries and model files that are themselves offline-capable (i.e. do not phone home at runtime).

---

## Notes

- **No default timeout.** `Transcribe` runs until the underlying subprocesses finish or the context is cancelled. Pass a `context.WithDeadline` or `context.WithTimeout` if you need bounded runtime.
- **Model is pluggable.** Set `Config.Model` to `chineseasr.ModelSenseVoice` (default, verified), `chineseasr.ModelParaformer`, or `chineseasr.ModelWhisper`. The arg builder is wired for all three; SenseVoice is the only model whose accuracy has been eyeball-verified in v1.
- **Output is Simplified Chinese with punctuation by default.** Punctuation is enabled via `--sense-voice-use-itn=1` for SenseVoice. Setting `Punctuation` to `nil` (or omitting it) enables punctuation; pass a pointer to `false` to disable it explicitly.
- **`New` validates paths but does not guarantee a working pipeline.** Call `Probe(ctx, sampleWavPath)` at startup with a known-good 16 kHz mono sample WAV to run a schema check against it and catch binary/model version drift early.
- **Model filename assumption (v1).** `ModelDir` must contain `tokens.txt` plus the model weights under the expected filenames: `model.int8.onnx` for sense-voice/paraformer, or `encoder.onnx` + `decoder.onnx` for whisper. If the downloaded package ships its `.onnx` files under different names, rename or symlink them to match — this is a documented v1 assumption.
