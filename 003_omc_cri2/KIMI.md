# Архитектурное решение: Система изучения китайского языка через live-радио

**Дата:** 2026-06-16
**Исследование:** Глубокий анализ протоколов потоковой передачи, ASR-движков, Android Media3 и NLP-библиотек

---

## 1. Рекомендуемая архитектура: HLS + WebSocket (Гибридный подход)

### 1.1 Почему не чистый WebSocket для аудио

WebSocket работает поверх TCP, который гарантирует доставку, но создает **Head-of-Line blocking**: если один аудио-пакет потерян, TCP ждет его ретрансляцию, задерживая все последующие пакеты. Это приводит к:
- **Замираниям звука** при потере пакетов
- **Накоплению задержки** (buffer bloat)
- **Сложности в управлении буфером** на клиенте

### 1.2 Почему HLS — оптимальный выбор для аудио

HLS (HTTP Live Streaming) работает поверх HTTP/TCP, но решает проблемы WebSocket через:

| Особенность | Описание |
|-------------|----------|
| **Сегментация** | Аудио разбивается на сегменты (3-10 сек), которые загружаются независимо |
| **Плейлист как "карта"** | `.m3u8` плейлист содержит список сегментов с точными таймингами |
| **DVR Window** | Сервер хранит N последних сегментов, позволяя перематывать |
| **Нативная поддержка** | Android Media3 ExoPlayer имеет встроенную, зрелую поддержку HLS |
| **BehindLiveWindowException** | Плеер автоматически обрабатывает выход за границы окна |

### 1.3 Гибридный подход: HLS для аудио + WebSocket для субтитров

```
+-------------------------------------------------------------+
|                        SERVER (Go)                          |
|  +--------------+  +--------------+  +------------------+  |
|  |  FFmpeg      |  |  HLS         |  |  WebSocket       |  |
|  |  Demuxer     |->|  Segmenter   |  |  Server          |  |
|  |  (m3u8 radio)|  |  (audio .ts) |  |  (subtitles JSON)|  |
|  +--------------+  +------+-------+  +------------------+  |
|                           |                                 |
|  +------------------------v------------------------------+  |
|  |           STREAMING PROCESSOR (Pipeline)              |  |
|  |  +----------+ -> +----------+ -> +----------+        |  |
|  |  | sherpa-  |    | go-ego/  |    | CC-      |        |  |
|  |  | onnx     |    | gse      |    | CEDICT   |        |  |
|  |  | ASR      |    | Segmenter|    | Pinyin   |        |  |
|  |  |          |    |          |    | + Meaning|        |  |
|  |  +----------+    +----------+    +----------+        |  |
|  +-------------------------------------------------------+  |
+-------------------------------------------------------------+
                              |
              +---------------+---------------+
              v               v               v
         +---------+    +----------+   +--------------+
         |  HLS    |    |  HLS     |   |  WebSocket   |
         | Playlist|    | Segments |   |  subtitles   |
         |.m3u8    |    |  .ts     |   |   JSON       |
         +---------+    +----------+   +--------------+
              |               |               |
              +---------------+---------------+
                              v
+-------------------------------------------------------------+
|                     ANDROID CLIENT                          |
|  +-------------------------------------------------------+  |
|  |  Media3 ExoPlayer                                     |  |
|  |  +------------+      +--------------------------+    |  |
|  |  | HLS Media  |      |  Custom SubtitleView     |    |  |
|  |  | Source     |      |  (hanzi + pinyin)        |    |  |
|  |  |            |      |                          |    |  |
|  |  | - Playlist |      | - Highlight current word |    |  |
|  |  |   loading  |      | - Word click -> popup    |    |  |
|  |  | - Buffering|      | - Sync by timestamp      |    |  |
|  |  | - Seeking  |      |                          |    |  |
|  |  | - DVR      |      +--------------------------+    |  |
|  |  |   handling |                ^                     |  |
|  |  +------------+                |                     |  |
|  |                      WebSocket subtitles             |  |
|  +-------------------------------------------------------+  |
+-------------------------------------------------------------+
```

---

## 2. Server (Go)

### 2.1 FFmpeg HLS Segmenter

```go
package hlsserver

type HLSSegmenter struct {
    OutputDir       string  // ~/tmp/china_radio_international/hls/
    SegmentDuration int     // 3 seconds (matches radio chunks)
    PlaylistWindow  int     // 360 segments = ~3 hours DVR window
    FFMpegCmd       *exec.Cmd
}
```

**FFmpeg arguments:**
```bash
ffmpeg -i https://sk.cri.cn/905.m3u8 \
  -codec:a aac -b:a 128k \
  -f hls \
  -hls_time 3 \
  -hls_list_size 360 \
  -hls_flags delete_segments+program_date_time \
  -hls_segment_filename "%03d.ts" \
  playlist.m3u8
```

**Why these settings:**
- `-hls_time 3` — matches the radio's 3-second audio chunks
- `-hls_list_size 360` — 360 segments x 3 sec = 3 hours (your TTL)
- `delete_segments` — old segments auto-deleted
- `program_date_time` — each segment tagged with exact time (critical for subtitle sync)

### 2.2 ASR Pipeline (sherpa-onnx + SenseVoice)

```go
package asr

type ASRProcessor struct {
    recognizer *sherpa.OfflineRecognizer  // Non-streaming for 3-sec chunks
    tokenizer  *gse.Segmenter
    dictionary *cccedict.Dictionary
}

func (p *ASRProcessor) ProcessChunk(audioPath string, startTime float64) (*TranscriptChunk, error) {
    // 1. Recognize via sherpa-onnx OfflineRecognizer
    wave := sherpa.ReadWave(audioPath)
    stream := sherpa.NewOfflineStream(p.recognizer)
    stream.AcceptWaveform(wave.SampleRate, wave.Samples)
    
    result := p.recognizer.GetResult(stream)
    hanziText := result.Text
    
    // 2. Get timestamps (SenseVoice supports CTC alignment since Nov 2024)
    charTimings := result.GetTimestamps() // per-character timings
    
    // 3. Segment into words via go-ego/gse
    words := p.tokenizer.Cut(hanziText, true)
    
    // 4. Lookup pinyin/meaning from CC-CEDICT for each word
    var wordEntries []WordEntry
    for _, word := range words {
        entry := p.dictionary.Lookup(word)
        timings := aggregateTimings(charTimings, word, hanziText)
        wordEntries = append(wordEntries, WordEntry{
            Word:      word,
            Pinyin:    entry.Pinyin,
            Meaning:   entry.Definitions[0],
            StartTime: startTime + timings.Start,
            EndTime:   startTime + timings.End,
        })
    }
    
    return &TranscriptChunk{
        StartTime: startTime,
        Duration:  3.0,
        Words:     wordEntries,
    }, nil
}
```

**Key findings from research:**
- sherpa-onnx supports **SenseVoice** via `OfflineRecognizer` with `--sense-voice-model` flag
- SenseVoice (since Nov 2024) supports **timestamp via CTC alignment** [^55^]
- Character-level timings can be aggregated to **word-level timings**
- Offline (non-streaming) mode is **faster and more accurate** for short 3-sec chunks than streaming mode

### 2.3 WebSocket Subtitle Server

```go
package wsserver

type SubtitleHub struct {
    clients    map[*Client]bool
    broadcast  chan TranscriptChunk
    register   chan *Client
    unregister chan *Client
    history    []TranscriptChunk  // Last 3 hours for new clients
}
```

**WebSocket message protocol:**

Server -> Client (new chunk):
```json
{
  "type": "chunk",
  "start_time": 1699123456.000,
  "duration": 3.0,
  "words": [
    {"word": "\u5927\u5bb6", "pinyin": "d\u00e0ji\u0101", "start": 0.0, "end": 0.8},
    {"word": "\u597d",   "pinyin": "h\u01ceo",   "start": 0.8, "end": 1.2}
  ]
}
```

Server -> Client (history for new connection):
```json
{
  "type": "history",
  "chunks": [...]
}
```

Client -> Server (pronounce request):
```json
{
  "type": "pronounce",
  "segment": "segment_1234.ts",
  "word": "\u5927\u5bb6",
  "start": 1.2,
  "end": 2.0
}
```

### 2.4 CC-CEDICT Dictionary Parser

```go
package dictionary

// CC-CEDICT format:
// \u5927\u5bb6 \u5927\u5bb6 [da4 jia1] /everyone/

type Entry struct {
    Traditional  string
    Simplified   string
    Pinyin       string
    Definitions  []string
}

type Dictionary struct {
    entries map[string]*Entry  // key: simplified word, O(1) lookup
}
```

**Research findings:**
- CC-CEDICT has **124,079+ entries** as of Nov 2025 [^23^]
- Go parser available: `github.com/purohit/go-cc-cedict` [^24^]
- Plain text format, easy to parse into hash-map

---

## 3. Android Client (Kotlin + Media3)

### 3.1 AudioPlayer (ExoPlayer + HLS)

```kotlin
class RadioPlayer(context: Context) {
    private val player: ExoPlayer = ExoPlayer.Builder(context)
        .setMediaSourceFactory(
            DefaultMediaSourceFactory(context)
                .setLiveTargetOffsetMs(3000)
        )
        .build()

    fun play(hlsUrl: String) {
        val mediaItem = MediaItem.Builder()
            .setUri(hlsUrl)
            .setLiveConfiguration(
                MediaItem.LiveConfiguration.Builder()
                    .setMaxPlaybackSpeed(1.02f)
                    .setMinPlaybackSpeed(0.98f)
                    .build()
            )
            .build()
        player.setMediaItem(mediaItem)
        player.prepare()
        player.play()
    }

    // Get current playback position as Unix timestamp for subtitle sync
    fun getCurrentPlaybackTime(): Long {
        val window = Timeline.Window()
        player.currentTimeline.getWindow(0, window)
        return window.windowStartTimeMs + player.currentPosition
    }

    // Handle BehindLiveWindowException
    private val listener = object : Player.Listener {
        override fun onPlayerError(error: PlaybackException) {
            if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                player.seekToDefaultPosition()
                player.prepare()
            }
        }
    }
}
```

**Research findings:**
- ExoPlayer fully supports **seeking in HLS live streams** [^37^]
- `seekTo(0)` seeks to start of live window, `seekToDefaultPosition()` to live edge [^37^]
- `BehindLiveWindowException` is the standard error when playback falls behind [^37^]
- Media3 1.8.0 includes **scrubbing mode** for smoother seeking [^19^]

### 3.2 SubtitleManager (WebSocket Client)

```kotlin
class SubtitleManager(private val serverUrl: String) {
    private val client = OkHttpClient()
    private var webSocket: WebSocket? = null
    private val subtitleCache = TreeMap<Long, TranscriptChunk>()

    fun connect() {
        val request = Request.Builder().url("$serverUrl/ws/subtitles").build()
        webSocket = client.newWebSocket(request, object : WebSocketListener() {
            override fun onMessage(webSocket: WebSocket, text: String) {
                val msg = Json.decodeFromString<WSMessage>(text)
                when (msg.type) {
                    "chunk" -> processChunk(msg)
                    "history" -> processHistory(msg)
                }
            }
        })
    }

    fun getActiveWords(timestamp: Long): List<WordEntry> {
        val entry = subtitleCache.floorEntry(timestamp) ?: return emptyList()
        val chunk = entry.value
        return chunk.words.filter { it.start <= timestamp && timestamp < it.end }
    }
}
```

### 3.3 Custom Subtitle View (Hanzi + Pinyin + Highlight)

```kotlin
class SubtitleView @JvmOverloads constructor(
    context: Context, attrs: AttributeSet? = null
) : View(context, attrs) {

    private var words: List<WordEntry> = emptyList()
    private var activeIndex: Int = -1
    private var showPinyin: Boolean = true

    private val hanziPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        textSize = 48f; color = Color.WHITE
    }
    private val pinyinPaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        textSize = 24f; color = Color.GRAY
    }
    private val activePaint = Paint(Paint.ANTI_ALIAS_FLAG).apply {
        color = Color.YELLOW
    }

    fun updateWords(newWords: List<WordEntry>, currentTime: Long) {
        words = newWords
        activeIndex = words.indexOfFirst { it.start <= currentTime && currentTime < it.end }
        invalidate()
    }

    override fun onDraw(canvas: Canvas) {
        super.onDraw(canvas)
        var x = 0f
        val y = height / 2f

        words.forEachIndexed { index, word ->
            val paint = if (index == activeIndex) activePaint else hanziPaint
            if (showPinyin) {
                canvas.drawText(word.pinyin, x, y - 30, pinyinPaint)
            }
            canvas.drawText(word.word, x, y + 20, paint)
            x += paint.measureText(word.word) + 20
        }
    }

    override fun onTouchEvent(event: MotionEvent): Boolean {
        if (event.action == MotionEvent.ACTION_UP) {
            val clickedWord = findWordAtPosition(event.x)
            clickedWord?.let { onWordClickListener?.invoke(it) }
        }
        return true
    }
}
```

### 3.4 Word Click -> Pause + Seek + Popup

```kotlin
fun onWordClick(word: WordEntry, isHighlighted: Boolean) {
    // Always pause playback
    player.pause()

    if (!isHighlighted) {
        // If clicking a non-highlighted word, seek to its start
        val window = Timeline.Window()
        player.currentTimeline.getWindow(0, window)
        val seekPosition = word.start - window.windowStartTimeMs
        player.seekTo(maxOf(0, seekPosition))
    }

    // Highlight the word
    subtitleView.highlightWord(word)

    // Show popup with word details
    WordPopup(context, anchor).show(word)
}
```

### 3.5 Audio Clip Extraction for Pronunciation

```kotlin
// Server endpoint: GET /api/audio/clip?segment=1234.ts&start=1.2&end=2.0
// Server extracts the word's audio from the original segment using ffmpeg

fun playPronunciation(word: WordEntry) {
    val url = "/api/audio/clip?segment=${word.segment}&start=${word.start}&end=${word.end}"
    val mediaItem = MediaItem.fromUri(url)
    pronunciationPlayer.setMediaItem(mediaItem)
    pronunciationPlayer.prepare()
    pronunciationPlayer.play()
}
```

Server-side clip extraction:
```bash
ffmpeg -i segment_1234.ts -ss 1.2 -t 0.8 -codec:a copy word_clip.aac
```

---

## 4. Client-Server Protocol

### 4.1 HLS Endpoint (Audio)

```
GET /hls/playlist.m3u8
  -> Returns HLS master playlist

GET /hls/{segment}.ts
  -> Returns AAC audio segment (MPEG-TS container)

Example playlist:
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:3
#EXT-X-MEDIA-SEQUENCE:1234
#EXT-X-PROGRAM-DATE-TIME:2024-01-15T10:30:00.000Z
#EXTINF:3.000,
segment_1234.ts
#EXTINF:3.000,
segment_1235.ts
```

### 4.2 WebSocket Endpoint (Subtitles)

```
WS /ws/subtitles

- Connection opened on app start
- Server immediately sends history (last N chunks)
- New chunks pushed as they become available
```

### 4.3 REST Endpoints

```
GET /api/audio/clip?segment={id}&start={sec}&end={sec}
  -> audio/aac (extracted clip for pronunciation)

POST /api/vocabulary
  {"word": "...", "pinyin": "...", "meaning": "...", "context": "..."}
  -> 200 OK

GET /api/health
  -> {"status": "ok", "asr_queue_size": N, "clients_connected": N}
```

---

## 5. Docker Architecture (Multi-stage)

```dockerfile
# ============================================================
# STAGE 1: Base image (all heavy dependencies)
# ============================================================
FROM golang:1.22-bookworm AS base

RUN apt-get update && apt-get install -y \
    ffmpeg \
    libonnxruntime-dev \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

ENV CGO_ENABLED=1
ENV CGO_LDFLAGS="-lonnxruntime"

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# Models and dictionaries (rarely change)
COPY models/ /app/models/
COPY dict/cedict_ts.u8 /app/dict/

# ============================================================
# STAGE 2: Go binary (only app code)
# ============================================================
FROM base AS builder
COPY . .
RUN go build -o /app/server ./cmd/server

# ============================================================
# STAGE 3: Runtime (minimal)
# ============================================================
FROM debian:bookworm-slim AS runtime

RUN apt-get update && apt-get install -y \
    ffmpeg \
    libonnxruntime1.17 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /app/server /app/
COPY --from=base /app/models/ /app/models/
COPY --from=base /app/dict/ /app/dict/

VOLUME ["/tmp/china_radio_international"]
EXPOSE 8080
CMD ["./server"]
```

**Benefits:**
- Base layer (Stage 1) rarely changes -> Docker caches it
- Rebuilding binary (Stage 2) takes seconds, not minutes
- Final image is minimal (debian:slim vs golang)

---

## 6. Pause/Resume with Seek Support

```kotlin
class PlaybackManager {
    private var pausedAtTimestamp: Long = 0
    private var isPaused = false

    fun pause() {
        player.pause()
        pausedAtTimestamp = getCurrentPlaybackTime()
        isPaused = true
    }

    fun resume() {
        if (!isPaused) return

        val window = Timeline.Window()
        player.currentTimeline.getWindow(0, window)

        if (pausedAtTimestamp < window.windowStartTimeMs) {
            // We've fallen behind the DVR window, start from beginning
            player.seekTo(0)
        } else {
            // Resume from where we paused
            player.seekTo(pausedAtTimestamp - window.windowStartTimeMs)
        }
        player.play()
        isPaused = false
    }
}
```

---

## 7. Why This Architecture Wins

| Aspect | Solution | Why It's Good |
|--------|----------|---------------|
| **Audio streaming** | HLS with DVR window | Battle-tested, native Android support, reliable buffering, seeking "for free" |
| **Subtitles** | WebSocket + JSON | Low latency, bidirectional (pronounce requests), simple format |
| **ASR** | sherpa-onnx SenseVoice | Local inference, fast for 3-sec chunks, timestamp support |
| **Segmentation** | go-ego/gse | Mature library, Chinese support, customizable dictionaries [^14^] |
| **Dictionary** | CC-CEDICT | 124K+ entries, open format, pinyin + English [^23^] |
| **Player** | Media3 ExoPlayer | Official Google library, mature, HLS-optimized |
| **Seeking** | HLS seeking + word timestamps | ExoPlayer handles seeking, word click = seekTo(word.start) |
| **Docker** | Multi-stage build | Fast rebuilds, minimal final image |

---

## 8. Alternatives Considered (and Rejected)

| Approach | Problem |
|----------|---------|
| Pure WebSocket for audio | TCP HoL blocking, buffer management nightmare, unreliable on mobile |
| DASH instead of HLS | More complex format, worse Media3 support for audio-only |
| Streaming ASR (real-time) | Streaming models have lower accuracy; offline mode is faster for 3-sec chunks |
| Custom protocol over TCP | Reinventing the wheel, solving all HLS problems from scratch |
| WebRTC | Overkill for one-way audio broadcast, complex setup |

---

## 9. Estimated Development Timeline

| Component | Estimated Time |
|-----------|---------------|
| Server: FFmpeg HLS segmenter | 1-2 days |
| Server: sherpa-onnx ASR pipeline | 3-5 days |
| Server: go-ego/gse + CC-CEDICT | 1-2 days |
| Server: WebSocket subtitle server | 2-3 days |
| Server: REST API (audio clips) | 1 day |
| Android: Media3 HLS player | 2-3 days |
| Android: WebSocket subtitle sync | 2-3 days |
| Android: Custom SubtitleView | 3-5 days |
| Android: Word popup + features | 2-3 days |
| Android: Settings screen | 0.5 day |
| Docker: Multi-stage build | 1 day |
| **Total** | **~3-4 weeks** |

---

## 10. References

- [^8^] sherpa-onnx GitHub: https://github.com/k2-fsa/sherpa-onnx
- [^12^] sherpa-onnx Go API docs: https://k2-fsa.github.io/sherpa/onnx/go-api/index.html
- [^14^] go-ego/gse: https://pkg.go.dev/github.com/go-ego/gse
- [^23^] CC-CEDICT format: https://grokipedia.com/page/cedict
- [^24^] go-cc-cedict parser: https://pkg.go.dev/github.com/purohit/go-cc-cedict
- [^37^] Media3 Live Streaming: https://developer.android.com/media/media3/exoplayer/live-streaming
- [^2^] HLS WebVTT Sync Guide: https://dev.to/masonwritescode/shipping-webvtt-subtitles-in-hls-that-actually-stay-in-sync
- [^15^] HLS Best Practices (ExoPlayer): https://medium.com/google-exoplayer/hls-playback-in-exoplayer
- [^55^] SenseVoice with timestamps: https://github.com/FunAudioLLM/SenseVoice
- [^5^] sherpa-onnx models mirror: https://modelscope.cn/models/ZhaoChaoqun/sherpa-onnx-asr-models

---

*Document generated after deep research of streaming protocols, ASR engines, Android Media3, and NLP libraries.*
