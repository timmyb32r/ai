# CRI Mobile Client

## Project Purpose

CRI (China International Radio) live-stream mobile client -- MP3 audio + synchronized Chinese subtitles with amber character highlighting. Connects to a Go-based backend server that streams PCM audio and SSE subtitle events.

## Architecture

```
UI(CriApp) --> ViewModel --> PlaybackStateMachine --> Services --> Infrastructure
                                    |
               +--------------------+--------------------+
               |                    |                    |
               v                    v                    v
      HttpAudioSource      SseSubtitleSource     AndroidAudioOutput
               |                    |                    |
               +--------+-----------+                    |
                        |                                |
                        v                                v
                 PCMRingBuffer                     AudioTrack
```

Live MP3 from server is decoded to PCM on the server side and streamed as raw s16le 16kHz mono PCM over HTTP. Subtitles arrive via SSE (Server-Sent Events). The playback loop reads PCM from the ring buffer and writes it to AudioTrack while matching subtitle positions against the playback cursor.

## Package Map

| Package | Contents | Purpose |
|---------|----------|---------|
| `api/` | `PlaybackStateMachine`, `AudioOutput`, `AudioStreamSource`, `SubtitleStreamSource`, `HighlightTracker` | Interfaces defining the service contracts |
| `service/` | `PlaybackStateMachineImpl`, `AndroidAudioOutput`, `HttpAudioSource`, `SseSubtitleSource`, `HighlightTrackerImpl` | Concrete implementations of API interfaces |
| `model/` | `PlaybackState` (enum), `SubtitleEvent`, `SyncedSegment`, `WordBoundary` | Data classes shared across layers |
| `infrastructure/` | `PCMRingBuffer` | Thread-safe 60s ring buffer for PCM audio data |
| `ui/` | `CriApp` | Jetpack Compose UI |
| (root) | `CriViewModel`, `MainActivity`, `ServerConfig`, `PlaybackService` | Android entry points and configuration |

## Key Interfaces

- **PlaybackStateMachine** -- Central coordinator. Exposes `state`, `segments`, `highlightRange` flows. Methods: `play()`, `pause()`, `resume()`, `stop()`, `onWordClick()`, `pronounceWord()`, `seekRelative()`.
- **AudioOutput** -- Wraps `android.media.AudioTrack`. Methods: `write()`, `play()`, `pause()`, `release()`.
- **AudioStreamSource** -- HTTP streaming source for PCM audio. Connects to server, writes decoded PCM into `PCMRingBuffer`.
- **SubtitleStreamSource** -- SSE source of timestamped subtitle events. Provides `connect(baseUrl)`, `disconnect()`, `drainPending()`.
- **HighlightTracker** -- Pure-math utility mapping play cursor to character range within a subtitle. Companion object has `calculateHighlightIndex()`, `findWordRange()`, `findWordByTime()`.

## Build

```bash
./gradlew assembleDebug
```

## Test

```bash
./gradlew test
```

## SDK & Dependencies

- **minSdk** 26, **targetSdk** 34, **compileSdk** 34
- **Jetpack Compose BOM** 2024.04.00
- **OkHttp** 4.12.0
- **Kotlin** 1.9.22
- **AGP** 8.2.2
- **Kotlinx Coroutines**, **AndroidX Lifecycle (ViewModel)**

## Server Dependency

Connects to the Go-based CRI broadcast relay server at `../001_omc_cri/`:

| Endpoint | Protocol | Description |
|----------|----------|-------------|
| `GET /v1/stream/audio` | HTTP chunked | Continuous PCM s16le 16kHz mono |
| `GET /v1/stream/subtitles` | SSE | SubtitleEvent JSON with word timestamps |
| `GET /v1/status` | JSON | `{delaySeconds: N}` for monitoring |

## Common AI Agent Tasks

### Add a new model class
1. Create file in `model/`
2. Reference it from the relevant `api/` interface
3. Implement in `service/`
4. Wire through `CriViewModel` if needed

### Add a new API interface
1. Define in `api/` with KDoc documenting thread-safety guarantees
2. Implement in `service/`
3. Inject via constructor in `PlaybackStateMachineImpl`
4. Surface state via `StateFlow` on `PlaybackStateMachine`

### Fix test compilation
- Tests import `com.crimobile.api.HighlightTracker` for `calculateHighlightIndex`, `findWordRange`, `findWordByTime` (moved from `CriViewModel` companion)
- `SubtitleEvent` and `WordBoundary` are in `com.crimobile.model`

### Remove dead code
- Check `grep -r` for references before deleting
- Update KDoc in `api/PlaybackStateMachine.kt` if interface/class references are removed
- Clean up imports in live files
