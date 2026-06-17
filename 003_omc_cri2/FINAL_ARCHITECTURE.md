# Финальная архитектура CRI Radio — систему изучения китайского языка через live-радио

**Дата:** 2026-06-16 (обновлено — реализация)
**Статус:** Сервер реализован (9/9 модулей, 42 теста зелёные). Клиент — в процессе.

---

## 1. Разрешение ключевых разногласий между предложениями

### 1.1. ASR-движок: whisper.cpp вместо SenseVoice 🔴

| Предложение | Позиция | Сила аргумента |
|-------------|---------|----------------|
| Deep Research | SenseVoice Go API **НЕ** даёт per-word тайминги (доказано 3× refuted claims: 0-3, 0-3, 1-2) | **Максимальная** — проверка исходного кода |
| ChatGPT | SenseVoice даёт per-character `timestamps: [0.72, 0.96, ...]` | Слабая — не подтверждено кодом |
| KIMI | SenseVoice поддерживает CTC alignment с Nov 2024 | Средняя — ссылается на changelog, но не на Go API |
| QWEN | SenseVoice в streaming mode с таймстемпами | Слабая — нет Go-specific подтверждения |

**Решение:** whisper.cpp (через Go bindings). whisper выдаёт per-segment timestamps и per-token вероятности из коробки. Для китайского whisper от OpenAI обучен на multilingual данных. Альтернатива — faster-whisper с CTranslate2.

**Выбранный движок:** [`whisper.cpp`](https://github.com/ggerganov/whisper.cpp) с Go bindings [`github.com/ggerganov/whisper.cpp/bindings/go`](https://github.com/ggerganov/whisper.cpp/tree/master/bindings/go).

Ключевые возможности whisper API:
```go
// whisper выдаёт per-segment тайминги:
type Segment struct {
    Start time.Duration  // timestamp начала сегмента
    End   time.Duration  // timestamp конца сегмента
    Text  string         // текст сегмента
    Tokens []Token       // per-token информация
}
type Token struct {
    Text string
    Start time.Duration
    End   time.Duration
    Probability float32
}
```

### 1.2. Протокол метаданных: SSE + GET /metadata (НЕ WebSocket, НЕ ID3)

| Предложение | Протокол | Плюсы | Минусы |
|-------------|----------|-------|--------|
| Deep Research | **SSE + GET /metadata** | Простота, HTTP-only, легко отлаживать | Нужен отдельный seek-эндпоинт |
| KIMI | WebSocket | Bidirectional, low latency | Сложный реконнект, Head-of-Line blocking |
| QWEN | ID3 in-band | Абсолютная синхронизация | Сложность генерации/парсинга, привязка к формату |
| ChatGPT | REST API | Простота | Нет push, polling |

**Решение:** SSE + GET /metadata. Причины:
1. Архитектурное требование пользователя: **ingest и клиенты разделены файловой системой.** Ingest пишет файлы → клиенты читают их через HTTP. Ни ingest, ни клиенты не знают друг о друге.
2. SSE — простейший способ push-уведомлений через HTTP. Работает через любой HTTP-клиент (OkHttp на Android).
3. GET /metadata?start=&end= — решает проблему seek (когда пользователь перематывает, SSE не содержит исторических данных).
4. WebSocket отвергнут потому что: требует persistent connection (сложный реконнект на мобильной сети) + избыточен (нам не нужна bidirectional коммуникация).
5. ID3 отвергнут потому что: генерация ID3 тегов в ffmpeg требует кастомных скриптов, парсинг на Android через Media3 нестабилен (legacy decoding disabled), и подход противоречит filesystem-based архитектуре.

### 1.3. HLS: ffmpeg как готовая библиотека

Все 4 предложения согласны: ffmpeg для генерации HLS. Это и есть "готовая библиотека" — не пишем HLS segmenter сами.

**Команда ffmpeg:**
```bash
ffmpeg -re -i https://sk.cri.cn/905.m3u8 \
  -codec:a aac -b:a 128k \
  -f hls \
  -hls_time 3 \
  -hls_list_size 3600 \
  -hls_flags delete_segments+program_date_time \
  -hls_segment_filename /tmp/china_radio_international/hls/%09d.ts \
  /tmp/china_radio_international/hls/playlist.m3u8
```

Параметры:
- `-hls_time 3` — 3-секундный сегмент
- `-hls_list_size 3600` — 3600 сегментов = 3 часа (TTL пользователя)
- `delete_segments` — автоудаление старых сегментов
- `program_date_time` — `#EXT-X-PROGRAM-DATE-TIME` в плейлисте (критично для синхронизации)

### 1.4. Docker: двухслойная схема (из 001_omc_cri)

Все 4 предложения предлагают multi-stage. **Лучшая имплементация — в 001_omc_cri** (уже работает).

Схема:
```
Dockerfile.base (редко меняется):
  ├─ python:3.12-slim-bookworm
  ├─ ffmpeg, ca-certificates
  ├─ whisper.cpp model (~1.5 GB для large-v3)
  ├─ CC-CEDICT словарь (~8 MB)
  └─ gse dictionaries (~1 MB)

Dockerfile (часто меняется):
  FROM criradio-base:latest
  ├─ go build (CGO_ENABLED=0)
  └─ COPY binary → final image
```

---

## 2. Финальная архитектура

### 2.1. Компонентная схема (top-level)

```
┌──────────────────────────────────────────────────────────────────┐
│                    SERVER (Go, Docker)                            │
│                                                                   │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  INGEST PIPELINE (один на сервер)                     │        │
│  │                                                       │        │
│  │  Radio URL ──► ffmpeg ──► PCM s16le ──► whisper.cpp  │        │
│  │                    │         16kHz mono    │           │        │
│  │                    │                       ▼           │        │
│  │                    │              TranscriptSegment   │        │
│  │                    │              (text + per-word     │        │
│  │                    │               timestamps)         │        │
│  │                    │                  │                │        │
│  │                    │                  ▼                │        │
│  │                    │           gse tokenizer           │        │
│  │                    │           → words[]               │        │
│  │                    │                  │                │        │
│  │                    │                  ▼                │        │
│  │                    │           CC-CEDICT lookup        │        │
│  │                    │           → pinyin + translation  │        │
│  │                    │                  │                │        │
│  │                    ▼                  ▼                │        │
│  │              ┌──────────────────────────────┐         │        │
│  │              │     OUTPUT: filesystem        │         │        │
│  │              │  ~/tmp/china_radio_international/       │        │
│  │              │                                      │        │
│  │              │  hls/                                │        │
│  │              │    ├─ playlist.m3u8                  │        │
│  │              │    ├─ 000000001.ts                   │        │
│  │              │    └─ ...                            │        │
│  │              │                                      │        │
│  │              │  metadata/                           │        │
│  │              │    ├─ index.json      (маппинг segment → timeline) │
│  │              │    ├─ 000000001.json  (JSON per segment) │        │
│  │              │    ├─ 000000002.json                  │        │
│  │              │    └─ ...                            │        │
│  │              │                                      │        │
│  │              │  TTL: 3 часа (автоудаление)          │        │
│  │              └──────────────────────────────┘         │        │
│  └──────────────────────────────────────────────────────┘        │
│                                                                   │
│  ┌──────────────────────────────────────────────────────┐        │
│  │  HTTP API (статические файлы + эндпоинты)             │        │
│  │                                                       │        │
│  │  GET /hls/playlist.m3u8    → файл из ФС               │        │
│  │  GET /hls/{segment}.ts     → файл из ФС               │        │
│  │  GET /api/metadata/{id}.json → файл из ФС             │        │
│  │  GET /api/subtitles        → SSE (новые .json файлы) │        │
│  │  GET /api/segment/audio?start=&end= → ffmpeg clip     │        │
│  │  GET /api/status           → JSON (health)           │        │
│  └──────────────────────────────────────────────────────┘        │
└──────────────────────────────────────────────────────────────────┘
                           │ HTTP (read-only)
                           ▼
┌──────────────────────────────────────────────────────────────────┐
│                    ANDROID CLIENT (Kotlin, Media3)                │
│                                                                   │
│  Media3 ExoPlayer          SSE Consumer           HTTP Client     │
│  (HLS playlist)            (metadata push)        (seek/clips)    │
│       │                         │                       │         │
│       ▼                         ▼                       ▼         │
│  AudioTrack              SubtitleBuffer            MetadataCache  │
│  (hardware decode)       (TreeMap<timeline,        (LRU, 10 min) │
│                           SubtitleSegment>)                       │
│       │                         │                                 │
│       └─────────┬───────────────┘                                 │
│                 ▼                                                  │
│        SubtitleSyncEngine (binary search)                         │
│        player.currentPosition → активный сегмент → активное слово │
│                 │                                                  │
│                 ▼                                                  │
│           CriViewModel (Compose StateFlow)                        │
│                 │                                                  │
│                 ▼                                                  │
│           CriApp (Jetpack Compose UI)                             │
└──────────────────────────────────────────────────────────────────┘
```

### 2.2. Файловая структура output-директории

Это ключевое архитектурное решение — **ingest и HTTP API разделены файловой системой**:

```
~/tmp/china_radio_international/
├── hls/                          # HLS сегменты (генерирует ffmpeg)
│   ├── playlist.m3u8             # HLS плейлист (обновляется ffmpeg)
│   ├── 000000001.ts              # AAC audio, 3 сек
│   ├── 000000002.ts
│   └── ...
│
├── metadata/                     # Метаданные (генерирует ingest pipeline)
│   ├── index.json                # Маппинг: segment_index → {timeline_start, timeline_end, ts_file, json_file}
│   ├── 000000001.json            # Субтитры для сегмента
│   ├── 000000002.json
│   └── ...
│
└── .lock                         # Флаг, что директория активна
```

**Формат `metadata/{segment}.json`:**
```json
{
  "segment_id": 1,
  "timeline_start_sec": 0.0,
  "timeline_end_sec": 3.0,
  "ts_file": "000000001.ts",
  "text_zh": "欢迎收听国际广播电台",
  "text_pinyin": "huānyíng shōutīng guójì guǎngbō diàntái",
  "text_en": "Welcome to listen to China Radio International",
  "words": [
    {
      "text": "欢迎",
      "char_start": 0,
      "char_end": 2,
      "start_sec": 0.0,
      "end_sec": 0.72,
      "pinyin": "huānyíng",
      "translation": "welcome"
    },
    {
      "text": "收听",
      "char_start": 2,
      "char_end": 4,
      "start_sec": 0.72,
      "end_sec": 1.44,
      "pinyin": "shōutīng",
      "translation": "listen to"
    }
  ]
}
```

**Формат `metadata/index.json`:**
```json
{
  "updated_at": "2026-06-16T18:00:00Z",
  "segments": [
    {"id": 1, "timeline_start_sec": 0.0, "timeline_end_sec": 3.0, "ts_file": "000000001.ts", "json_file": "000000001.json"},
    {"id": 2, "timeline_start_sec": 3.0, "timeline_end_sec": 6.0, "ts_file": "000000002.ts", "json_file": "000000002.json"}
  ]
}
```

### 2.3. Модульная декомпозиция (Go server)

| Модуль | Назначение | Интерфейс (Go interface) | Зависимости |
|--------|------------|--------------------------|-------------|
| **`internal/ingest`** | Запуск ffmpeg → HLS + PCM | `Ingestor { Start(ctx) error; Stop() error; PCMChan() <-chan []float32 }` | ffmpeg |
| **`internal/asr`** | whisper.cpp транскрибация | `Transcriber { Transcribe(pcm []float32, segmentID int) (*TranscriptSegment, error) }` | whisper.cpp |
| **`internal/tokenizer`** | gse сегментация на слова | `Tokenizer { Segment(text string) []Word }` | gse |
| **`internal/dictionary`** | CC-CEDICT поиск | `Dictionary { Lookup(word string) (*Entry, error); LookupPinyin(word string) string }` | CC-CEDICT |
| **`internal/pipeline`** | Оркестрация ingest→ASR→tokenizer→dict→output | `Pipeline { Run(ctx context.Context) error }` | Все выше |
| **`internal/storage`** | Запись/чтение metadata JSON + TTL cleanup | `MetadataStore { Write(seg TranscriptSegment) error; Read(segID int) (*TranscriptSegment, error); ReadRange(start, end float64) ([]TranscriptSegment, error); Cleanup(ttl time.Duration) }` | ФС |
| **`internal/api`** | HTTP API: static files + SSE + endpoints | Стандартный `http.Handler` | storage |

### 2.4. Модульная декомпозиция (Android client)

| Модуль | Назначение | Интерфейс (Kotlin interface) | Зависимости |
|--------|------------|------------------------------|-------------|
| **`player`** | Media3 ExoPlayer + HLS воспроизведение | `RadioPlayer { play(); pause(); seekTo(timelineMs: Long); val currentTimelineMs: StateFlow<Long>; val playbackState: StateFlow<PlaybackState> }` | Media3 |
| **`subtitles`** | SSE consumer + metadata cache | `SubtitleSource { val segments: StateFlow<List<SubtitleSegment>>; connect(); disconnect() }` | OkHttp |
| **`sync`** | Синхронизация позиции → сегмент → слово | `SubtitleSyncEngine { fun findActiveSegment(timelineMs: Long): SubtitleSegment?; fun findActiveWord(segment: SubtitleSegment, timelineMs: Long): WordEntry?; fun findSegmentForWord(word: WordEntry): SubtitleSegment? }` | — |
| **`pronounce`** | Извлечение и проигрывание фрагмента аудио | `PronunciationPlayer { fun playWord(word: WordEntry, segment: SubtitleSegment) }` | Media3, PCM cache |
| **`vocabulary`** | Сохранение слов в файл | `VocabularyStore { fun appendWord(word: WordEntry) }` | MediaStore |
| **`ui`** | Jetpack Compose UI | `CriApp(state: CriViewState, onAction: (CriAction) -> Unit)` | Compose |
| **`viewmodel`** | Состояние UI + оркестрация | `CriViewModel { val state: StateFlow<CriViewState>; fun dispatch(action: CriAction) }` | Все выше |

---

## 3. Протоколы и форматы данных

### 3.1. HLS (аудио)

```
GET /hls/playlist.m3u8
  → Content-Type: application/vnd.apple.mpegurl

#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:3
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-PROGRAM-DATE-TIME:2026-06-16T18:00:00.000Z
#EXTINF:3.000,
000000001.ts
#EXT-X-PROGRAM-DATE-TIME:2026-06-16T18:00:03.000Z
#EXTINF:3.000,
000000002.ts
...
```

**Таймлайн:** `#EXT-X-PROGRAM-DATE-TIME` из плейлиста даёт абсолютную временную привязку. Media3 предоставляет `window.windowStartTimeMs` для синхронизации.

### 3.2. SSE (субтитры, реальное время)

```
GET /api/subtitles
  → Content-Type: text/event-stream

event: sync
data: {"timeline_start_sec": 0.0, "server_time": "2026-06-16T18:00:00Z"}

event: segment
data: {"segment_id": 1, "timeline_start_sec": 0.0, "timeline_end_sec": 3.0,
       "ts_file": "000000001.ts", "text_zh": "欢迎...", "words": [...]}

event: segment
data: {"segment_id": 2, ...}
```

SSE события отправляются **каждый раз, когда новый metadata/*.json появляется в директории.** Серверный модуль API делает `fsnotify` (или poll) директории и пушит новые файлы через SSE.

### 3.3. GET /api/segment/audio (произношение слова)

```
GET /api/segment/audio?ts_file=000000001.ts&start_sec=0.0&end_sec=0.72
  → Content-Type: audio/aac
  → Тело: AAC-фрагмент (вырезан ffmpeg -ss ... -t ...)
```

Сервер вызывает:
```bash
ffmpeg -ss 0.0 -t 0.72 -i /tmp/.../hls/000000001.ts -codec:a copy -f adts -
```

### 3.4. GET /api/status

```json
{
  "status": "running",
  "channel_url": "https://sk.cri.cn/905.m3u8",
  "segments_total": 120,
  "oldest_segment_id": 1,
  "newest_segment_id": 120,
  "metadata_files": 120,
  "hls_files": 120,
  "live_edge_offset_sec": 180.0,
  "clients_connected": 3
}
```

---

## 4. Docker-сборка (два слоя, быстрая пересборка)

### Dockerfile.base (редко меняется)
```dockerfile
FROM debian:bookworm-slim

# Системные зависимости
RUN apt-get update && apt-get install -y --no-install-recommends \
    ffmpeg ca-certificates wget curl \
    && rm -rf /var/lib/apt/lists/*

# whisper.cpp модель (large-v3, ~1.5 GB)
# Скачивается один раз при билде базового образа
ARG WHISPER_MODEL=ggml-large-v3.bin
RUN mkdir -p /opt/models \
    && wget -q https://huggingface.co/ggerganov/whisper.cpp/resolve/main/${WHISPER_MODEL} \
       -O /opt/models/${WHISPER_MODEL}

# CC-CEDICT словарь
COPY .docker-cache/cedict_ts.u8 /opt/cedict_ts.u8

# gse словари
COPY .docker-cache/gse-dict/ /opt/gse-dict/

# whisper.cpp бинарник
COPY .docker-cache/whisper-cli /usr/local/bin/whisper-cli

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- "http://127.0.0.1:8080/api/status" >/dev/null 2>&1 || exit 1
```

### Dockerfile (часто меняется — только Go код)
```dockerfile
ARG GO_VERSION=1.24
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/criradio-server ./cmd/server

FROM criradio-base:latest
COPY --from=build /out/criradio-server /usr/local/bin/criradio-server

ENV MODEL_PATH=/opt/models/ggml-large-v3.bin \
    CEDICT_PATH=/opt/cedict_ts.u8 \
    GSE_DICT_PATH=/opt/gse-dict \
    OUTPUT_DIR=/tmp/china_radio_international \
    CHANNEL_URL=https://sk.cri.cn/905.m3u8 \
    DELAY=180s

ENTRYPOINT ["/usr/local/bin/criradio-server"]
```

### docker-build.sh (как в 001_omc_cri)
```bash
#!/bin/bash
set -eu

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE_IMAGE="criradio-base:latest"
SERVER_IMAGE="${IMAGE:-criradio-server}"

# Проверяем/билдим base если надо
if ! docker images -q "$BASE_IMAGE" 2>/dev/null | grep -q .; then
    echo "==> Building base image..."
    docker build -f "$SCRIPT_DIR/Dockerfile.base" -t "$BASE_IMAGE" "$SCRIPT_DIR"
fi

# Билдим сервер (быстро — только Go код)
echo "==> Building server image..."
docker build -t "$SERVER_IMAGE" "$@"
echo "Done: $SERVER_IMAGE"
```

---

## 5. Ключевые corner-кейсы для HLS

### 5.1. Пауза > DVR окна
- Пользователь нажал pause, через 4 часа нажал play
- DVR окно = 3 часа → позиция паузы уже удалена из плейлиста
- **Решение:** `BehindLiveWindowException` → клиент делает `seekToDefaultPosition()` (live edge) с clear-индикацией "Вы были вне эфира, начинаем с live"

### 5.2. Потеря сети на мобильном
- Клиент теряет WiFi/LTE на 30 секунд
- **Решение:** Media3 встроенный buffer (настроить `setMinBufferMs(15000)`) + авто-реконнект
- SSE при обрыве: клиент делает reconnect, получает `sync` + последние сегменты

### 5.3. Seek в середину сегмента
- Пользователь тапнул на слово, которое внутри 3-сек TS сегмента
- **Решение:** `seekTo()` на `window.windowStartTimeMs + word.start_sec * 1000`
- Media3 сам загрузит нужный TS-сегмент и начнёт с правильного байтового смещения

### 5.4. Граница сегментов HLS
- Слово пересекает границу двух TS-сегментов (начало во втором 974, конец в третьем 032)
- **Решение:** whisper даёт абсолютные тайминги. Даже если слово на границе — его таймстемпы корректны, клиент использует абсолютную шкалу

### 5.5. ffmpeg переподключение к radio HLS
- Исходный HLS stream может обрываться
- **Решение:** ffmpeg с `-reconnect 1 -reconnect_at_eof 1 -reconnect_streamed 1 -reconnect_delay_max 30`
- При reconnect — новый `#EXT-X-DISCONTINUITY` в плейлисте — клиент должен сбросить timeline

---

## 6. Стратегия логирования

### Правила
- Максимум 10 строк/сек на все модули вместе
- Метаинформация, не данные (не логировать аудио/PCM)
- Сервер: stdout (контейнер)
- Клиент: Android Logcat (тэг `CRIRadio`)

### Формат логов
```
[I] {timestamp} {module} {event} {key=value ...}
[D] {timestamp} {module} {event} {key=value ...}  // DEBUG only
[W] {timestamp} {module} {event} {key=value ...}  // WARNING
[E] {timestamp} {module} {event} {key=value ...}  // ERROR
```

### Что логировать

| Модуль | События | Частота |
|--------|---------|---------|
| **ingest** | ffmpeg started/stopped/reconnect, bytes_written, hls_segment_count | ~1/3s |
| **asr** | segment_id, duration_ms, model_latency_ms, text_len | ~1/3s |
| **tokenizer** | segment_id, word_count (batch: 1 строка на 5 сегментов) | ~1/15s |
| **dictionary** | segment_id, lookup_count, miss_count (batch) | ~1/15s |
| **storage** | file_written, file_deleted (TTL), total_files | ~1/60s |
| **api/sse** | client_connected, client_disconnected, segments_pushed | по факту |
| **pipeline** | segment pipeline: ingest→asr→tokenize→dict→store latency | ~1/3s |

### Пример логов
```
[I] 18:00:00.000 ingest ffmpeg_started pid=12345 channel=https://sk.cri.cn/905.m3u8
[I] 18:00:03.123 pipeline seg=1 ingest_ms=80 asr_ms=450 tokenize_ms=2 dict_ms=5 store_ms=1 total_ms=538
[I] 18:00:06.234 pipeline seg=2 ingest_ms=82 asr_ms=420 tokenize_ms=2 dict_ms=6 store_ms=1 total_ms=511
[I] 18:00:09.345 pipeline seg=3 ingest_ms=81 asr_ms=445 tokenize_ms=1 dict_ms=7 store_ms=1 total_ms=535
[I] 18:01:00.000 storage ttl_cleanup deleted=180 kept=360 total_files=540
[D] 18:01:00.000 storage batch_summary segs_processed=18 words_total=234 dict_hits=230 dict_misses=4
[I] 18:01:00.001 api sse_client_connected total_clients=3
```

---

## 7. Использованные источники из 4-х предложений

### От Deep Research (самые надёжные — adversarial verification):
1. sherpa-onnx Go API source code — подтверждено отсутствие per-token таймингов
2. Media3 1.4.0 legacy decoding break — подтверждено GitHub issues
3. Media3 1.8.0 scrubbing mode — подтверждено Android Developers Blog
4. gse performance (9.2 MB/s) — подтверждено README
5. cedict Go library — подтверждено pkg.go.dev

### От KIMI (практические детали):
6. ExoPlayer `BehindLiveWindowException` handling
7. FFmpeg `program_date_time` для HLS синхронизации
8. WebSocket SubtitleHub паттерн (архитектурная идея)

### От QWEN (HLS теория):
9. ID3 in-band metadata механизм (не использован, но изучен)
10. HLS `#EXT-X-PROGRAM-DATE-TIME` timeline

### От ChatGPT (базовые паттерны):
11. REST API для query субтитров по времени
12. Базовый UI flow (пауза → seek → подсветка)

---

## 8. Технологический стек (финальный)

| Слой | Технология | Обоснование |
|------|-----------|-------------|
| **ASR** | whisper.cpp (Go bindings) | Per-word timestamps, открытый код, Go-совместим |
| **HLS ingest** | ffmpeg (подпроцесс) | Готовая, проверенная библиотека |
| **HLS serve** | Go `http.FileServer` | Без nginx — проще, меньше движущихся частей |
| **Токенизация** | go-ego/gse | 9.2 MB/s, зрелая библиотека |
| **Словарь** | Ecostack/cedict | Go-native CC-CEDICT парсер |
| **Протокол** | HLS + SSE + REST | HTTP-only, без WebSocket |
| **Android аудио** | Media3 ExoPlayer 1.8.0+ | Scrubbing mode, HLS нативно |
| **Android UI** | Jetpack Compose + Material3 | Современный, декларативный |
| **Android HTTP** | OkHttp 4.12+ | SSE, HTTP/2 |
| **Сервер DI** | Ручной (wire-like) | Без фреймворков |
| **Клиент DI** | Hilt | Стандарт для Android |
| **Тесты (server)** | Go std `testing` + `httptest` | Без внешних зависимостей |
| **Тесты (client)** | JUnit 5 + MockK + Compose Testing | Стандарт для Android |
| **Docker** | Multi-stage (base + app) | Быстрая пересборка |
