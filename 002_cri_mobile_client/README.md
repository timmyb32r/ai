# CRI Mobile Client

Android client for **China International Radio (CRI)** live-stream listening with synchronized Chinese subtitle display.

## What It Does

- Plays live MP3 audio stream from a CRI broadcast relay server
- Displays synchronized Chinese subtitles (pinyin and English translation optional)
- Amber character highlighting tracks the current playback position word-by-word
- Tap a word to pause and see its pinyin/English
- Tap the speaker icon to hear the word pronounced (using cached PCM audio)
- Background playback via Android foreground service

## Build & Install

```bash
# Build debug APK
./gradlew assembleDebug

# Install on connected device
./gradlew installDebug

# Run tests
./gradlew test
```

The APK is produced at `app/build/outputs/apk/debug/app-debug.apk`.

## Architecture Overview

The app uses a layered architecture:

```
UI Layer (Compose)  -->  ViewModel  -->  PlaybackStateMachine  -->  Services/Infrastructure
```

- **CriApp** (Jetpack Compose) -- full UI with subtitle view, playback controls, word popup
- **CriViewModel** -- AndroidX ViewModel bridging UI to the state machine
- **PlaybackStateMachineImpl** -- orchestrates PCM audio playback + subtitle syncing in a threaded loop
- **HttpAudioSource** -- streams PCM audio from the server into a ring buffer
- **SseSubtitleSource** -- receives subtitle events via Server-Sent Events
- **AndroidAudioOutput** -- wraps `android.media.AudioTrack`
- **PCMRingBuffer** -- 60-second thread-safe circular buffer for raw PCM
- **HighlightTracker** -- maps playback position to highlighted character range

## Server Connection

The app connects to a Go backend server. Enter the server host and port in the settings dialog (accessible from the main screen).

The server must provide:
- `GET /v1/stream/audio` -- continuous PCM s16le 16kHz mono audio stream
- `GET /v1/stream/subtitles` -- SSE stream of `SubtitleEvent` JSON objects with word-level timestamps
- `GET /v1/status` -- JSON with `delaySeconds` for monitoring

Server implementation: [github.com/timmyb32r/omc-cri](https://github.com/timmyb32r/omc-cri) (see the server's README for deployment instructions).
