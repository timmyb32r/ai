# Deep Interview Spec: Local Chinese Speech-Recognition Go Library

## Metadata
- Interview ID: di-zh-asr-20260613
- Rounds: 8
- Final Ambiguity Score: 17%
- Type: greenfield
- Generated: 2026-06-13
- Threshold: 0.2 (20%)
- Threshold Source: default
- Initial Context Summarized: no
- Status: PASSED

## Clarity Breakdown
| Dimension | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Goal Clarity | 0.90 | 0.40 | 0.360 |
| Constraint Clarity | 0.85 | 0.30 | 0.255 |
| Success Criteria | 0.72 | 0.30 | 0.216 |
| **Total Clarity** | | | **0.831** |
| **Ambiguity** | | | **0.169 (17%)** |

(Greenfield weights: Goal 40%, Constraints 30%, Criteria 30%. No Context dimension.)

## Topology
| Component | Status | Description | Coverage / Deferral Note |
|-----------|--------|-------------|--------------------------|
| Audio Input & Decoding | active | Accept the audio file and normalize it for the engine | Accepts common formats (mp3/m4a/wav/flac/…) at any rate/channels; internally converts to 16 kHz mono via an ffmpeg subprocess (Round 5) |
| Local ASR Model | active | Run the best open-source Chinese ASR model offline | Engine = sherpa-onnx; default model = SenseVoice-Small; model is pluggable via config to Paraformer-Large / FireRedASR / Qwen3-ASR / Whisper (Round 3) |
| Go ↔ Model Integration | active | How Go drives the model | Go shells out to the `sherpa-onnx-offline` CLI and parses its output (subprocess). Pure-Go dropped; in-process CGo considered and declined (Rounds 2, 4) |
| Transcript Output | active | Emit recognized Chinese characters | v1 returns a plain Chinese-text string (with punctuation) behind a forward-compatible result struct so segments/timestamps/metadata can be added later without breaking callers (Round 6) |

## Goal
Build a **reusable Go library/package** (not a CLI-first tool) that takes a path to an audio file containing Chinese speech, transcribes it **fully locally/offline**, and returns the recognized speech as **Chinese characters (text string)**.

Internally the library: (1) uses **ffmpeg** to decode and resample arbitrary input audio to 16 kHz mono WAV; (2) invokes the **`sherpa-onnx-offline` CLI** as a subprocess running the **SenseVoice-Small** model (model selectable via config); (3) parses the CLI output and returns it as a result struct whose primary field is the transcript string. Paths to `ffmpeg`, `sherpa-onnx-offline`, and the model directory are supplied by the caller as configuration parameters.

## Constraints
- **Language/runtime:** Go library; recognition performed by external processes (subprocess architecture), not pure Go and not in-process CGo.
- **Engine:** sherpa-onnx (k2-fsa), invoked via its `sherpa-onnx-offline` command-line binary.
- **Default model:** SenseVoice-Small (zh/yue/en/ja/ko, built-in punctuation, fast on CPU). Model must be **pluggable** via config (Paraformer-Large, FireRedASR, Qwen3-ASR, Whisper as documented alternatives).
- **Audio input:** Accept common formats at any sample rate / channel count; library normalizes to 16 kHz mono via an ffmpeg subprocess.
- **Offline:** Must run fully offline. No network access at runtime (no auto-download).
- **Provisioning:** Paths to `ffmpeg`, `sherpa-onnx-offline`, and the SenseVoice model dir are passed as **explicit config parameters**. The library does not download or bundle them; it must fail with a clear, actionable error if a configured tool/model is missing.
- **Target OS (primary):** Linux and macOS.
- **Output (v1):** Plain Chinese-character string with punctuation, returned via an extensible result type.
- **Documentation (required deliverable):** Instructions that cover (1) what each path parameter should point to, (2) which external tools/models are required (SenseVoice model, `sherpa-onnx-offline`, ffmpeg), (3) how to install them on macOS and Linux.

## Non-Goals
- No pure-Go inference (explicitly dropped).
- No in-process CGo bindings (considered and declined in favor of subprocess looseness).
- No real-time/streaming transcription in v1 (batch, file-in → text-out).
- No CLI as the primary deliverable (a thin demo/example CLI is optional, not the product).
- No segment timestamps, word timestamps, diarization, or emotion/event metadata in v1 (API designed to allow them later).
- No auto-download or vendoring/bundling of binaries or model files.
- No labeled-dataset CER benchmarking required for v1 acceptance.
- Windows is not a primary target for v1 (sherpa-onnx supports it, but it is out of initial scope).

## Acceptance Criteria
- [ ] Given a real Chinese-speech audio file in a common format (mp3/m4a/wav/flac), the library returns a Chinese-character transcript that a Chinese reader judges correct/usable.
- [ ] The full pipeline runs **fully offline** (no network calls at runtime).
- [ ] Input audio at arbitrary sample rate/channels is correctly normalized to 16 kHz mono (via ffmpeg) before recognition.
- [ ] The model used is configurable; switching from SenseVoice-Small to another supported model is a config change, not a code change.
- [ ] Paths to `ffmpeg`, `sherpa-onnx-offline`, and the model directory are accepted as config parameters; a missing/invalid path produces a clear, actionable error (not a panic or opaque failure).
- [ ] The public API returns a result struct (e.g. `Result{ Text string }`) so that segments/timestamps/metadata can be added later without breaking callers.
- [ ] No crashes on a handful of representative real recordings; malformed/empty/unsupported input yields a descriptive error.
- [ ] Documentation exists covering: (1) what each path parameter points to, (2) the required external tools/models, (3) macOS + Linux install steps for SenseVoice, `sherpa-onnx-offline`, and ffmpeg.

## Assumptions Exposed & Resolved
| Assumption | Challenge | Resolution |
|------------|-----------|------------|
| "Go program" implies a CLI | Asked usage shape | Primary deliverable is a reusable **library/package**, not a CLI (Round 1) |
| "In Go" implies pure-Go inference | Asked how Go performs recognition; explained quality comes from the model, not the language; pure Go strands you on weak/old models and slow inference | Pure Go **dropped**; recognition delegated to an external engine (Rounds 2, user Q&A) |
| Best Chinese model = Whisper | Provided 2026 benchmark data (SenseVoice/Paraformer/FireRedASR beat Whisper on Mandarin CER) | Engine = **sherpa-onnx**, default **SenseVoice-Small**, pluggable to higher-accuracy models (Round 3) |
| Subprocess is required because CGo needs a C toolchain | **Contrarian:** sherpa-onnx ships *prebuilt* CGo libs (no toolchain), runs in-process, is the recommended path | User reaffirmed **subprocess (CLI)** for looser coupling / easy engine swap (Round 4) |
| Caller passes ready-to-use WAV | Asked who normalizes audio to 16 kHz mono | Library **auto-converts** common formats via an ffmpeg subprocess (Round 5) |
| Rich structured output is needed | **Simplifier:** what is the simplest valuable output? | v1 = **plain text string**, behind an **extensible** result type (Round 6) |
| Needs a formal accuracy benchmark | Asked for the acceptance bar | **Eyeball real samples** offline; usable transcript, no crashes (Round 7) |
| Tooling auto-installed/bundled | Asked provisioning + target OS | **Explicit path config params**, fully offline; **Linux + macOS**; **install docs required** (Round 8) |

## Technical Context (greenfield)
- **Engine:** [sherpa-onnx](https://k2-fsa.github.io/sherpa/onnx/index.html) (k2-fsa). Offline, no network; uses onnxruntime under the hood. Provides a `sherpa-onnx-offline` CLI suitable for subprocess invocation.
- **Model:** [SenseVoice-Small](https://huggingface.co/FunAudioLLM/SenseVoiceSmall) — non-autoregressive, ~15× faster than Whisper, multilingual (zh/yue/en/ja/ko), built-in punctuation; pretrained sherpa-onnx package e.g. `sherpa-onnx-sense-voice-zh-en-ja-ko-yue-int8-*`. CLI flags map to `--sense-voice-model`, `--tokens`, `--sense-voice-language zh`, `--num-threads`.
- **Higher-accuracy swap-ins (config only):** Paraformer-Large (tiny, near-SOTA Mandarin CER), [FireRedASR](https://github.com/FireRedTeam/FireRedASR) (SOTA-class Mandarin CER ~3.05–3.18%), Qwen3-ASR (newest 2026 SOTA), Whisper-large-v3 (broad multilingual). All supported as sherpa-onnx offline model configs.
- **Audio decode/resample:** ffmpeg subprocess → 16 kHz mono WAV (PCM s16le) before passing to the engine.
- **Process model:** Go `os/exec` to invoke ffmpeg, then `sherpa-onnx-offline`; capture stdout; parse the recognized text out of the CLI output.
- **Config surface:** explicit paths for `ffmpeg`, `sherpa-onnx-offline`, and the model directory/files; model/language selection; thread count.
- **Sources:** [Gladia – best open-source STT 2026](https://www.gladia.io/blog/best-open-source-speech-to-text-models), [sherpa-onnx Go package](https://pkg.go.dev/github.com/k2-fsa/sherpa-onnx-go-linux), [sherpa-onnx SenseVoice models](https://k2-fsa.github.io/sherpa/onnx/sense-voice/pretrained.html).

## Ontology (Key Entities) — final round
| Entity | Type | Fields | Relationships |
|--------|------|--------|---------------|
| AudioFile | core domain | path, format, sampleRate, channels, duration | Input to Transcriber; normalized by AudioConverter |
| Transcript / Result | core domain | text (Chinese chars), [future: segments, language, metadata] | Produced by Transcriber |
| ASRModel | supporting | name (SenseVoice/Paraformer/…), language, sizeOnDisk, modelDir | Selected by TranscriberConfig; run by ASREngineProcess |
| Transcriber | core domain | Transcribe(audioPath) → Result; config | Orchestrates AudioConverter + ASREngineProcess |
| ASREngineProcess | external system | binaryPath (sherpa-onnx-offline), args, stdout | Runs ASRModel; invoked by Transcriber as subprocess |
| AudioConverter | external system | ffmpegPath, targetRate=16000, channels=1 | Converts AudioFile → 16 kHz mono WAV |
| TranscriberConfig | supporting | ffmpegPath, sherpaOfflinePath, modelDir, model, language, numThreads | Configures Transcriber, ASREngineProcess, AudioConverter |

## Ontology Convergence
| Round | Entity Count | New | Changed | Stable | Stability Ratio |
|-------|-------------|-----|---------|--------|----------------|
| 1 | 5 | 5 | - | - | N/A |
| 2 | 5 | 0 | 1 | 4 | 100% |
| 3 | 5 | 0 | 0 | 5 | 100% |
| 4 | 5 | 0 | 0 | 5 | 100% |
| 5 | 6 | 1 | 0 | 5 | 83% |
| 6 | 6 | 0 | 0 | 6 | 100% |
| 7 | 6 | 0 | 0 | 6 | 100% |
| 8 | 7 | 1 | 0 | 6 | 86% |

The domain model converged early (a stable core of AudioFile/Transcript/ASRModel/Transcriber/Engine by round 2) with two deliberate additions — `AudioConverter` (ffmpeg, round 5) and `TranscriberConfig` (path params, round 8) — as constraints were resolved.

## Residual assumptions (defaults; confirm if wrong)
- **Script variant:** output in **Simplified Chinese** (SenseVoice default). Traditional output (e.g. via OpenCC) is a future option, not v1.
- **Dialect scope:** **Mandarin** is the primary target; SenseVoice also supports Cantonese (yue) as a bonus, not a v1 requirement.

## Interview Transcript
<details>
<summary>Full Q&A (8 rounds + 1 user-initiated clarification)</summary>

### Round 0 — Topology
Confirmed 4 top-level components: Audio Input & Decoding, Local ASR Model, Go ↔ Model Integration, Transcript Output. ("Looks right (all 4)")

### Round 1 — Goal (usage shape)
**Q:** What does a single run look like, and on what audio?
**A:** Library/API, not a CLI — a reusable Go package other code calls.
**Ambiguity:** 65% (Goal 0.55, Constraints 0.25, Criteria 0.20)

### Round 2 — Constraints (integration)
**Q:** How should Go perform the recognition (pure Go / CGo / ONNX / subprocess)?
**A:** Go orchestrates a subprocess.
**Ambiguity:** 56% (Goal 0.60, Constraints 0.45, Criteria 0.22)

### User clarification
**Q (user):** If we make everything pure Go, will quality be much lower than a Python/C++ subprocess?
**A (Claude):** Quality comes from the model, not the language; pure Go strands you on weak/old models + slow inference; the clean "no Python" path is CGo (whisper.cpp/sherpa-onnx), but subprocess is fine.
**User decision:** Drop pure-Go goal; keep Python/C++ subprocess.

### Round 3 — Model selection (delegated)
**Q:** Which engine/model for offline Mandarin?
**A:** "You recommend the best" → sherpa-onnx + SenseVoice-Small default, model pluggable.
**Ambiguity:** 48% (Goal 0.70, Constraints 0.55, Criteria 0.25)

### Round 4 — Contrarian (integration revisited)
**Q:** sherpa-onnx ships prebuilt CGo libs (no toolchain, in-process). Keep subprocess or switch?
**A:** Keep subprocess (CLI).
**Ambiguity:** 44% (Goal 0.75, Constraints 0.60, Criteria 0.28)

### Round 5 — Audio input
**Q:** What audio to accept, and who normalizes to 16 kHz mono?
**A:** Accept common formats; auto-convert via ffmpeg.
**Ambiguity:** 39% (Goal 0.78, Constraints 0.68, Criteria 0.30)

### Round 6 — Simplifier (output shape)
**Q:** Simplest valuable output?
**A:** Text now, structured later (extensible API).
**Ambiguity:** 32% (Goal 0.85, Constraints 0.70, Criteria 0.42)

### Round 7 — Success criteria
**Q:** What's the acceptance bar for v1?
**A:** Eyeball real samples — correct/usable transcripts, offline, no crashes.
**Ambiguity:** 22% (Goal 0.88, Constraints 0.72, Criteria 0.70)

### Round 8 — Provisioning + OS
**Q:** How are ffmpeg / sherpa-onnx-offline / model obtained, and which OS?
**A:** Paths passed as explicit config params; Linux & macOS; install docs required.
**Ambiguity:** 17% (Goal 0.90, Constraints 0.85, Criteria 0.72) — PASSED

</details>
