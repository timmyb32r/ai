# План разработки CRI Radio — декомпозиция на модули, интерфейсы, приёмка

**Дата:** 2026-06-16
**Принцип:** Модуль за модулем. Приёмка каждого модуля = прохождение acceptance-тестов. Только после приёмки — межмодульная интеграция.

---

## Карта зависимостей модулей

```
                    ┌──────────┐
                    │  Models  │  (общие типы данных — нулевой модуль, без зависимостей)
                    └────┬─────┘
                         │
          ┌──────────────┼──────────────┐
          ▼              ▼              ▼
    ┌──────────┐  ┌──────────┐  ┌──────────────┐
    │Tokenizer │  │Dictionary│  │Config/Logging│  (независимые — можно параллельно)
    └────┬─────┘  └────┬─────┘  └──────────────┘
         │              │
         └──────┬───────┘
                ▼
         ┌──────────┐
         │   ASR    │  (whisper.cpp wrapper — зависит от Models)
         └────┬─────┘
              │
              ▼
    ┌──────────────────┐
    │     Storage      │  (файловая система — зависит от Models)
    └────────┬─────────┘
             │
             ▼
    ┌──────────────────┐
    │     Ingest       │  (ffmpeg wrapper — зависит от Models)
    └────────┬─────────┘
             │
             ▼
    ┌──────────────────┐
    │    Pipeline       │  (оркестрация — зависит от всех выше)
    └────────┬─────────┘
             │
             ▼
    ┌──────────────────┐
    │      API          │  (HTTP — зависит от Storage)
    └────────┬─────────┘
             │
             ▼
    ┌──────────────────┐
    │   Integration    │  (межмодульные тесты, E2E)
    └──────────────────┘

SERVER (Go) завершён.

Параллельно с Go-модулями можно делать:

ANDROID:
    ┌──────────┐  ┌──────────┐  ┌──────────────┐
    │  Models  │  │  Player  │  │  Vocabulary  │  (независимые)
    └────┬─────┘  └────┬─────┘  └──────────────┘
         │              │
         └──────┬───────┘
                ▼
         ┌──────────┐
         │Subtitles │  (SSE + cache — зависит от Models)
         └────┬─────┘
              │
              ▼
         ┌──────────┐
         │  Sync    │  (SubtitleSyncEngine — зависит от Models)
         └────┬─────┘
              │
              ▼
         ┌──────────┐
         │Pronounce │  (AudioTrack + clip fetch)
         └────┬─────┘
              │
              ▼
         ┌──────────┐
         │ViewModel │  (оркестрация UI-состояния)
         └────┬─────┘
              │
              ▼
         ┌──────────┐
         │    UI    │  (Jetpack Compose)
         └──────────┘
```

---

## Фаза 0: Общие модели данных (Module 00-models)

### Назначение
Типы данных, разделяемые всеми модулями. Компилируется без внешних зависимостей.

### Go: `internal/models/models.go`
```go
package models

// TranscriptSegment — результат обработки одного 3-сек аудио-сегмента
type TranscriptSegment struct {
    SegmentID        int         `json:"segment_id"`
    TimelineStartSec float64     `json:"timeline_start_sec"`
    TimelineEndSec   float64     `json:"timeline_end_sec"`
    TSFile           string      `json:"ts_file"`
    TextZh           string      `json:"text_zh"`
    TextPinyin       string      `json:"text_pinyin"`
    TextEn           string      `json:"text_en"`
    Words            []WordEntry `json:"words"`
}

type WordEntry struct {
    Text        string  `json:"text"`
    CharStart   int     `json:"char_start"`
    CharEnd     int     `json:"char_end"`
    StartSec    float64 `json:"start_sec"`
    EndSec      float64 `json:"end_sec"`
    Pinyin      string  `json:"pinyin"`
    Translation string  `json:"translation"`
}

type SegmentIndex struct {
    UpdatedAt string           `json:"updated_at"`
    Segments  []SegmentRef     `json:"segments"`
}

type SegmentRef struct {
    ID               int     `json:"id"`
    TimelineStartSec float64 `json:"timeline_start_sec"`
    TimelineEndSec   float64 `json:"timeline_end_sec"`
    TSFile           string  `json:"ts_file"`
    JSONFile         string  `json:"json_file"`
}

type PipelineStats struct {
    SegmentID     int   `json:"segment_id"`
    IngestMs      int64 `json:"ingest_ms"`
    ASRMs         int64 `json:"asr_ms"`
    TokenizeMs    int64 `json:"tokenize_ms"`
    DictMs        int64 `json:"dict_ms"`
    StorageMs     int64 `json:"storage_ms"`
    TotalMs       int64 `json:"total_ms"`
}

// ServerStatus — ответ /api/status
type ServerStatus struct {
    Status              string `json:"status"`
    ChannelURL          string `json:"channel_url"`
    SegmentsTotal       int    `json:"segments_total"`
    MetadataFiles       int    `json:"metadata_files"`
    LiveEdgeOffsetSec   float64 `json:"live_edge_offset_sec"`
    ClientsConnected    int    `json:"clients_connected"`
}
```

### Kotlin: `model/` пакет
```kotlin
// Эквивалентные data classes
data class SubtitleSegment(
    val segmentId: Int,
    val timelineStartSec: Double,
    val timelineEndSec: Double,
    val tsFile: String,
    val textZh: String,
    val textPinyin: String,
    val textEn: String,
    val words: List<WordEntry>
)

data class WordEntry(
    val text: String,
    val charStart: Int,
    val charEnd: Int,
    val startSec: Double,
    val endSec: Double,
    val pinyin: String,
    val translation: String
)
```

### Приёмка (acceptance tests)
- [x] `TestModelsSerialization` — JSON marshal/unmarshal round-trip для TranscriptSegment (Go)
- [x] `TestModelsValidation` — TimelineStartSec < TimelineEndSec, CharStart < CharEnd, Words не пуст
- [x] `TestSegmentRefIntegrity` — SegmentRef.ID уникален, JSONFile и TSFile не пусты
- [x] Kotlin: data class equality, JSON parsing tests

---

## Фаза 1: Независимые серверные модули (параллельно)

---

### Module 01-tokenizer: gse wrapper

**Интерфейс:**
```go
// internal/tokenizer/tokenizer.go
package tokenizer

type Tokenizer interface {
    // Segment разбивает китайский текст на слова
    Segment(text string) []Token
    // Close освобождает ресурсы
    Close() error
}

type Token struct {
    Text      string // слово
    CharStart int    // индекс первого символа в исходном тексте
    CharEnd   int    // индекс после последнего символа
}
```

**Имплементация:** `internal/tokenizer/gse_tokenizer.go` — обёртка над `github.com/go-ego/gse`

**Приёмка:**
- [x] `TestSegmentSimple` — "欢迎收听国际广播电台" → ["欢迎", "收听", "国际", "广播", "电台"]
- [x] `TestSegmentWithPunctuation` — пунктуация отделяется ("你好！" → ["你好", "！"])
- [x] `TestSegmentEmpty` — пустая строка → пустой слайс
- [x] `TestSegmentNumbers` — "2024年" → ["2024", "年"]
- [x] `TestCharIndices` — `Token.CharStart` и `Token.CharEnd` корректно указывают на позиции в исходном тексте
- [x] `TestBenchmark` — throughput > 1 MB/s (gse обещает 9.2, проверяем минимум)

---

### Module 02-dictionary: CC-CEDICT wrapper

**Интерфейс:**
```go
// internal/dictionary/dictionary.go
package dictionary

type Dictionary interface {
    // Lookup ищет слово в CC-CEDICT
    Lookup(simplified string) (*Entry, error)
    // LookupPinyin возвращает только пиньинь (быстрее)
    LookupPinyin(simplified string) string
    // Stats возвращает статистику хитов/промахов
    Stats() Stats
    // Close освобождает ресурсы
    Close() error
}

type Entry struct {
    Traditional string
    Simplified  string
    Pinyin      string
    Meanings    []string
}

type Stats struct {
    Hits  int64
    Misses int64
    Total int64
}
```

**Имплементация:** `internal/dictionary/cedict_dict.go` — парсит `cedict_ts.u8` в `map[string]*Entry` при инициализации

**Используемая библиотека:** `github.com/Ecostack/cedict` (подтверждена Deep Research как рабочая, 3-0 голосование)

**Приёмка:**
- [x] `TestLookupKnown` — "中国" → pinyin "Zhōngguó", meanings содержит "China"
- [x] `TestLookupUnknown` — несуществующее слово → error
- [x] `TestLookupPinyin` — "你好" → "nǐhǎo"
- [x] `TestLookupTraditional` — "中國" (традиционный) → тот же результат что и "中国"
- [x] `TestLoadTime` — загрузка cedict_ts.u8 менее 2 секунд (123K записей)
- [x] `TestStats` — после 10 Lookup Hits+Misses = Total

---

### Module 03-config: Конфигурация + логирование

**Интерфейс:**
```go
// internal/config/config.go
package config

type Config struct {
    ChannelURL string        // https://sk.cri.cn/905.m3u8
    OutputDir  string        // ~/tmp/china_radio_international
    ModelPath  string        // путь к ggml-large-v3.bin
    DictPath   string        // путь к cedict_ts.u8
    GSEDictDir string        // путь к gse словарям
    HLSTime    int           // 3 (секунды на сегмент)
    HLSWindow  int           // 3600 (сегментов в окне = 3 часа)
    Delay      time.Duration // 180s задержка
    Addr       string        // :8080
    LogLevel   string        // info | debug | warn
}

func FromEnv() *Config
func (c *Config) Validate() error
```

```go
// internal/logging/logging.go
package logging

type Logger interface {
    Info(module, event string, kv ...interface{})
    Debug(module, event string, kv ...interface{})
    Warn(module, event string, kv ...interface{})
    Error(module, event string, kv ...interface{})
}

// NewProductionLogger — stdout, формат [I] time module event key=value
func NewProductionLogger(level string) Logger
```

**Приёмка:**
- [x] `TestConfigDefaults` — все значения по умолчанию валидны
- [x] `TestConfigValidate` — пустой OutputDir → ошибка, пустой ModelPath → ошибка
- [x] `TestLoggerFormat` — вывод содержит `[I]`, timestamp, module, event
- [x] `TestLoggerRateLimit` — не более 10 строк/сек (batch-тест с 1000 вызовов за 100ms)

---

## Фаза 2: ASR модуль (whisper.cpp)

### Module 04-asr: whisper.cpp Go wrapper

**Важно! Перед имплементацией проверить что whisper.cpp Go bindings действительно выдают per-word тайминги!**

**Интерфейс:**
```go
// internal/asr/asr.go
package asr

import "github.com/yourorg/criradio/internal/models"

type Transcriber interface {
    // Transcribe выполняет распознавание PCM-данных
    // pcm: float32 samples, 16kHz mono
    // segmentID: идентификатор сегмента для логов
    Transcribe(pcm []float32, segmentID int) (*models.TranscriptSegment, error)
    // Close освобождает ресурсы (whisper.cpp модель)
    Close() error
}

// TranscriberConfig — параметры whisper
type TranscriberConfig struct {
    ModelPath  string  // путь к ggml-large-v3.bin
    Language   string  // "zh" (авто-определение если пусто)
    Threads    int     // кол-во потоков CPU
    MaxContext int     // максимальный контекст (по умолчанию -1 = авто)
}
```

**Имплементация:** `internal/asr/whisper_transcriber.go`

**Алгоритм:**
1. Принять `[]float32` PCM (16kHz mono)
2. Вызвать `whisper_full(whisper_context, params, samples, n_samples)`
3. Получить `whisper_full_n_segments(ctx)` → количество сегментов
4. Для каждого сегмента `i`:
   - `whisper_full_get_segment_text(ctx, i)` → текст
   - `whisper_full_get_segment_t0(ctx, i)` → start time (в 10ms единицах)
   - `whisper_full_get_segment_t1(ctx, i)` → end time
   - `whisper_full_n_tokens(ctx, i)` → количество токенов
   - Для каждого токена `j`:
     - `whisper_full_get_token_text(ctx, i, j)` → текст токена
     - `whisper_full_get_token_t0(ctx, i, j)` → start time
     - `whisper_full_get_token_t1(ctx, i, j)` → end time
5. Собрать `TranscriptSegment` с per-word таймингами

**Приёмка:**
- [x] `TestWhisperModelLoad` — модель загружается без ошибок, `ctx != nil`
- [x] **`TestWhisperTimestamps`** — **САМЫЙ ВАЖНЫЙ ТЕСТ:** транскрибировать тестовый WAV с известным текстом. Проверить что per-token тайминги присутствуют и корректны (слово "欢迎" начинается в ~0.0-0.5s, не в 0 для всей фразы)
- [x] `TestWhisperChinese` — китайская речь распознаётся с приемлемым качеством (проверить хотя бы 50% слов правильно)
- [x] `TestWhisperSilence` — тишина → пустой текст, нет паники
- [x] `TestWhisperShortAudio` — 500ms аудио → обрабатывается без ошибок
- [x] `TestWhisperLongAudio` — 10s аудио → обрабатывается без ошибок
- [x] `TestWhisperConcurrent` — 2 параллельных вызова Transcribe (разные PCM) → оба успешны
- [x] `TestWhisperClose` — Close() освобождает память, повторный вызов Close() безопасен

---

## Фаза 3: Storage модуль

### Module 05-storage: Файловое хранилище метаданных

**Интерфейс:**
```go
// internal/storage/storage.go
package storage

import "github.com/yourorg/criradio/internal/models"

type MetadataStore interface {
    // Write сохраняет TranscriptSegment в metadata/{segment_id}.json
    // и обновляет metadata/index.json
    Write(segment *models.TranscriptSegment) error

    // Read читает сегмент по ID
    Read(segmentID int) (*models.TranscriptSegment, error)

    // ReadRange читает все сегменты в диапазоне [startSec, endSec]
    ReadRange(startSec, endSec float64) ([]models.TranscriptSegment, error)

    // ReadIndex читает текущий index.json
    ReadIndex() (*models.SegmentIndex, error)

    // Cleanup удаляет сегменты старше ttl
    Cleanup(ttl time.Duration) (deleted int, err error)

    // Watch возвращает канал с новыми SegmentRef (для SSE)
    Watch(ctx context.Context) (<-chan models.SegmentRef, error)

    // Stats возвращает статистику хранилища
    Stats() StorageStats
}

type StorageStats struct {
    TotalFiles   int
    OldestID     int
    NewestID     int
    DirSizeBytes int64
}
```

**Имплементация:** `internal/storage/fs_store.go`

**Алгоритм Watch:** Использовать `fsnotify` (или poll с интервалом 500ms) для отслеживания новых `metadata/*.json` файлов.

**Приёмка:**
- [x] `TestWriteRead` — записать сегмент, прочитать обратно, данные совпадают
- [x] `TestReadRange` — записать 10 сегментов (0-3s, 3-6s, ...), ReadRange(3.0, 9.0) → 2 сегмента
- [x] `TestReadRangeEmpty` — ReadRange в диапазоне без данных → пустой слайс
- [x] `TestIndexIntegrity` — после 5 Write, ReadIndex содержит 5 SegmentRef, сортировка по ID
- [x] `TestCleanup` — записать 5 сегментов, выставить ttl=0, cleanup удаляет все 5
- [x] `TestCleanupPartial` — записать 5 сегментов с разным временем, ttl удаляет только старые 2
- [x] `TestWatch` — запустить Watch, записать сегмент, из канала приходит SegmentRef
- [x] `TestConcurrentWrite` — 10 параллельных Write → все успешны, index корректен

---

## Фаза 4: Ingest модуль

### Module 06-ingest: ffmpeg HLS захват

**Интерфейс:**
```go
// internal/ingest/ingest.go
package ingest

type Ingestor interface {
    // Start запускает ffmpeg подпроцесс
    Start(ctx context.Context) error
    // Stop останавливает ffmpeg
    Stop() error
    // PCMChan возвращает канал с PCM-данными для ASR
    // (один float32 слайс = 3 секунды аудио)
    PCMChan() <-chan PCMChunk
    // Stats возвращает статистику ingest
    Stats() IngestStats
}

type PCMChunk struct {
    SegmentID   int
    Samples     []float32 // PCM float32, 16kHz mono, ~48000 сэмплов на 3 сек
    DurationSec float64
    Error       error
}

type IngestStats struct {
    SegmentsIngested int64
    BytesWritten     int64
    FFMpegPID        int
    Running          bool
}
```

**Имплементация:** `internal/ingest/ffmpeg_ingest.go`

**Два выхода ffmpeg (одновременно):**
1. **HLS сегменты** → `outputDir/hls/` (через `-f hls`)
2. **PCM pipe** → stdout ffmpeg (через `-f f32le -acodec pcm_f32le -ac 1 -ar 16000 -`)

```bash
ffmpeg -re -reconnect 1 -reconnect_at_eof 1 -reconnect_streamed 1 \
       -reconnect_delay_max 30 \
       -i https://sk.cri.cn/905.m3u8 \
       -codec:a aac -b:a 128k \
       -f hls -hls_time 3 -hls_list_size 3600 \
       -hls_flags delete_segments+program_date_time \
       -hls_segment_filename /tmp/.../hls/%09d.ts \
       /tmp/.../hls/playlist.m3u8 \
       -f f32le -acodec pcm_f32le -ac 1 -ar 16000 pipe:1
```

**Приёмка:**
- [x] `TestFFMpegStartStop` — запустить ffmpeg, проверить что процесс запущен, остановить, процесс завершился
- [x] `TestHLSOutput` — через 6 секунд в outputDir/hls есть playlist.m3u8 и минимум 2 .ts файла
- [x] `TestPCMOutput` — PCMChan получает чанки (~48000 float32 samples каждый)
- [x] `TestReconnect` — симулировать обрыв HLS источника (закрыть порт), ffmpeg переподключается
- [x] `TestStopWhileRunning` — Stop() во время работы не вызывает панику, каналы закрываются
- [x] `TestHLSTimeline` — плейлист содержит `#EXT-X-PROGRAM-DATE-TIME`

---

## Фаза 5: Pipeline (оркестрация)

### Module 07-pipeline: Связка всех модулей

**Интерфейс:**
```go
// internal/pipeline/pipeline.go
package pipeline

type Pipeline interface {
    // Run запускает главный цикл обработки
    Run(ctx context.Context) error
    // Stats возвращает агрегированную статистику
    Stats() PipelineStats
}

type PipelineStats struct {
    SegmentsTotal   int64
    SegmentsPerMin  float64
    AvgASRLatencyMs int64
    AvgDictHitRate  float64
}
```

**Имплементация:** `internal/pipeline/runner.go`

**Главный цикл:**
```go
func (p *runner) Run(ctx context.Context) error {
    go p.ingestor.Start(ctx)

    for {
        select {
        case <-ctx.Done():
            return p.shutdown()
        case chunk := <-p.ingestor.PCMChan():
            if chunk.Error != nil {
                p.logger.Warn("pipeline", "pcm_error", "err", chunk.Error)
                continue
            }
            go p.processChunk(ctx, chunk)
        }
    }
}

func (p *runner) processChunk(ctx context.Context, chunk PCMChunk) {
    t0 := time.Now()

    // 1. ASR
    asrStart := time.Now()
    segment, err := p.transcriber.Transcribe(chunk.Samples, chunk.SegmentID)
    asrMs := time.Since(asrStart).Milliseconds()

    // 2. Tokenize
    tokStart := time.Now()
    var words []models.WordEntry
    for _, t := range p.tokenizer.Segment(segment.TextZh) {
        // Ищем пиньинь + перевод
        entry, err := p.dictionary.Lookup(t.Text)
        // ... собираем WordEntry с правильными таймингами от whisper
    }
    tokMs := time.Since(tokStart).Milliseconds()

    // 3. Store
    storeStart := time.Now()
    p.store.Write(segment)
    storeMs := time.Since(storeStart).Milliseconds()

    // 4. Log
    totalMs := time.Since(t0).Milliseconds()
    p.logger.Info("pipeline", "segment_done",
        "id", chunk.SegmentID,
        "asr_ms", asrMs,
        "tok_ms", tokMs,
        "store_ms", storeMs,
        "total_ms", totalMs,
    )
}
```

**Приёмка:**
- [x] `TestPipelineOneSegment` — mock'и всех зависимостей, один PCMChunk → вызваны Transcriber, Tokenizer, Dictionary, Storage
- [x] `TestPipelineGracefulShutdown` — контекст отменён → Pipeline.Run() завершается без горутин-сирот
- [x] `TestPipelineErrorRecovery` — Transcriber вернул ошибку → Pipeline продолжает работу, не падает
- [x] `TestPipelineStats` — после обработки 10 чанков Stats().SegmentsTotal == 10

---

## Фаза 6: HTTP API

### Module 08-api: HTTP эндпоинты

**Интерфейс:** стандартный `http.Handler`

**Эндпоинты:**
```go
// internal/api/api.go
func NewRouter(store MetadataStore, config Config, logger Logger) http.Handler

// Регистрирует:
// GET /hls/                  → http.FileServer (hls директория)
// GET /api/metadata/{id}.json → http.FileServer (metadata директория)
// GET /api/subtitles         → SSE handler
// GET /api/segment/audio     → ffmpeg clip extractor
// GET /api/status            → JSON status
```

**SSE handler:**
```go
func (a *API) handleSubtitles(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    flusher := w.(http.Flusher)

    // Отправить sync
    fmt.Fprintf(w, "event: sync\ndata: {...}\n\n")
    flusher.Flush()

    // Подписаться на Watch
    ch, _ := a.store.Watch(r.Context())
    for {
        select {
        case <-r.Context().Done():
            return
        case ref := <-ch:
            // Читать metadata/{id}.json и отправить как SSE
            seg, _ := a.store.Read(ref.ID)
            json, _ := json.Marshal(seg)
            fmt.Fprintf(w, "event: segment\ndata: %s\n\n", json)
            flusher.Flush()
        }
    }
}
```

**Приёмка:**
- [x] `TestHLSPlaylist` — GET /hls/playlist.m3u8 → 200, Content-Type: application/vnd.apple.mpegurl
- [x] `TestHLSPlaylistNotFound` — нет файла → 404
- [x] `TestMetadataJSON` — GET /api/metadata/1.json → 200, валидный JSON TranscriptSegment
- [x] `TestMetadataNotFound` — GET /api/metadata/99999.json → 404
- [x] `TestSSESync` — GET /api/subtitles → получаем `event: sync` первым
- [x] `TestSSENewSegment` — mock Watch канал → приход нового SegmentRef → SSE `event: segment`
- [x] `TestAudioClip` — GET /api/segment/audio?ts_file=xxx.ts&start_sec=0.5&end_sec=1.0 → 200, audio/aac
- [x] `TestAudioClipMissingParams` — GET /api/segment/audio → 400
- [x] `TestStatus` — GET /api/status → 200, валидный ServerStatus JSON
- [x] `TestCORS` — заголовки Access-Control-Allow-Origin присутствуют

---

## Фаза 7: Интеграционные тесты сервера

### Module 09-integration: End-to-end серверные тесты

**Приёмка:**
- [x] `TestFullPipeline` — запустить сервер с тестовым аудиофайлом → через 15 секунд в outputDir есть .ts, .m3u8, metadata/*.json
- [x] `TestClientFlow` — HTTP клиент: GET /hls/playlist.m3u8 → GET /hls/000000001.ts → GET /api/metadata/1.json → данные корректны
- [x] `TestSSEFlow` — открыть SSE, подождать 10 секунд → получить минимум 2 события segment
- [x] `TestTTLCleanup` — заполнить 10 сегментов, выставить ttl=1s, подождать → осталось 0
- [x] `TestConcurrentClients` — 5 одновременных SSE подключений → все получают sync + segments
- [x] `TestServerGracefulShutdown` — SIGTERM → сервер корректно завершает все горутины
- [x] `GoBuild` — `go build ./cmd/server` успешен
- [x] `GoTest` — `go test ./...` все тесты зелёные
- [x] `GoVet` — `go vet ./...` без ошибок

---

## Фаза 8: Android — независимые модули

---

### Module 10-android-models: Общие модели данных

**Файлы:** `model/SubtitleSegment.kt`, `model/WordEntry.kt`, `model/PlaybackState.kt`

**Приёмка:**
- [x] `SubtitleSegmentJsonTest` — JSON deserialization из файла-примера
- [x] `WordEntryEqualityTest` — data class equality работает
- [x] `PlaybackStateTransitionsTest` — все валидные переходы IDLE→LOADING→PLAYING→PAUSED→PLAYING

---

### Module 11-android-player: Media3 ExoPlayer HLS

**Интерфейс:**
```kotlin
// player/RadioPlayer.kt
interface RadioPlayer {
    val currentTimelineMs: StateFlow<Long>
    val playbackState: StateFlow<PlaybackState>
    val behindLiveWindow: StateFlow<Boolean>

    fun play(hlsUrl: String)
    fun pause()
    fun seekTo(timelineMs: Long)
    fun seekToLiveEdge()
    fun release()
}

enum class PlaybackState { IDLE, LOADING, PLAYING, PAUSED, ERROR }
```

**Имплементация:** `player/ExoRadioPlayer.kt` — обёртка над Media3 ExoPlayer

**Важные настройки ExoPlayer:**
```kotlin
ExoPlayer.Builder(context)
    .setMediaSourceFactory(
        DefaultMediaSourceFactory(context)
            .setLiveTargetOffsetMs(3000) // 3s от live edge
    )
    .setScrubbingModeEnabled(true) // Media3 1.8.0+
    .build()
```

**Приёмка:**
- [x] `TestPlayerPlay` — play() → state = LOADING → PLAYING
- [x] `TestPlayerPause` — pause() → state = PAUSED, currentTimelineMs не меняется
- [x] `TestPlayerSeek` — seekTo(X) → currentTimelineMs ≈ X (допуск ±500ms)
- [x] `TestPlayerBehindLiveWindow` — симулировать (mock HLS с коротким окном) → behindLiveWindow = true
- [x] `TestPlayerSeekToLiveEdge` — seekToLiveEdge() → позиция у live edge
- [x] `TestPlayerRelease` — release() → последующий play() не крашит (создаётся новый плеер)

---

### Module 12-android-vocabulary: Сохранение слов

**Интерфейс:**
```kotlin
// vocabulary/VocabularyStore.kt
interface VocabularyStore {
    fun appendWord(word: WordEntry, context: String)
    suspend fun getSavedWords(): List<String>
}
```

**Имплементация:** `vocabulary/FileVocabularyStore.kt` — запись в `Downloads/cri_vocabulary.txt` через MediaStore API

**Приёмка:**
- [x] `TestAppendWord` — слово добавляется в файл с переводом строки
- [x] `TestReadBack` — getSavedWords() возвращает ранее добавленные слова
- [x] `TestEmptyFile` — getSavedWords() на новом файле → пустой список

---

## Фаза 9: Android — зависимые модули

---

### Module 13-android-subtitles: SSE consumer + кеш

**Интерфейс:**
```kotlin
// subtitles/SubtitleSource.kt
interface SubtitleSource {
    val segments: StateFlow<List<SubtitleSegment>>
    val connected: StateFlow<Boolean>

    fun connect(serverUrl: String)
    fun disconnect()
}
```

**Имплементация:** `subtitles/SseSubtitleSource.kt` — OkHttp SSE client

**Кеш:** TreeMap<Long, SubtitleSegment> (ключ — timelineStartMs)

**Приёмка:**
- [x] `TestConnect` — connect() → connected = true
- [x] `TestReceiveSync` — первое SSE событие "sync" → segments обновляется
- [x] `TestReceiveSegment` — SSE "segment" → segment добавляется в список
- [x] `TestDisconnect` — disconnect() → connected = false
- [x] `TestReconnect` — disconnect → connect → получаем sync заново
- [x] `TestCacheEviction` — больше 100 сегментов → старые вытесняются

---

### Module 14-android-sync: SubtitleSyncEngine

**Интерфейс:**
```kotlin
// sync/SubtitleSyncEngine.kt
class SubtitleSyncEngine(
    private val segments: List<SubtitleSegment>
) {
    fun findActiveSegment(timelineMs: Long): SubtitleSegment?
    fun findActiveWord(segment: SubtitleSegment, timelineMs: Long): WordEntry?
    fun findSegmentContainingWord(word: WordEntry): SubtitleSegment?
    fun findWordTimelineMs(word: WordEntry, segment: SubtitleSegment): Long
}
```

**Алгоритм:** Binary search по `segments` (отсортированы по `timelineStartSec`), затем binary search по `words` внутри сегмента.

**Приёмка:**
- [x] `TestFindActiveSegmentExact` — timelineMs = 1.5s, сегмент 0.0-3.0 → найден
- [x] `TestFindActiveSegmentBoundary` — timelineMs = 3.0 (граница) → правильный сегмент
- [x] `TestFindActiveSegmentBeforeFirst` — timelineMs = -1.0 → null
- [x] `TestFindActiveSegmentAfterLast` — timelineMs = 999999.0 → последний сегмент
- [x] `TestFindActiveWord` — timelineMs попадает в [word.startSec, word.endSec] → найден
- [x] `TestFindActiveWordBetween` — timelineMs между словами → предыдущее слово активно
- [x] `TestFindSegmentContainingWord` — слово из середины списка → правильный сегмент
- [x] `TestFindWordTimelineMs` — timelineMs слова = parentSegment.timelineStartSec + word.startSec
- [x] `TestBinarySearchPerformance` — 1000 сегментов, поиск < 1ms

---

### Module 15-android-pronounce: Произношение слова

**Интерфейс:**
```kotlin
// pronounce/PronunciationPlayer.kt
interface PronunciationPlayer {
    fun playWord(word: WordEntry, segment: SubtitleSegment)
    fun stop()
    fun release()
}

// pronounce/AudioClipFetcher.kt
interface AudioClipFetcher {
    suspend fun fetchClip(tsFile: String, startSec: Double, endSec: Double): ByteArray
}
```

**Имплементация:** 
- `AudioClipFetcher` — OkHttp запрос `GET /api/segment/audio?ts_file=...`
- `PronunciationPlayer` — `AudioTrack` MODE_STATIC с PCM данными

**Приёмка:**
- [x] `TestFetchClip` — mock сервер возвращает AAC данные → fetchClip возвращает ByteArray
- [x] `TestPlayWord` — AudioTrack проигрывает PCM без исключений
- [x] `TestStop` — stop() останавливает воспроизведение мгновенно
- [x] `TestRelease` — release() освобождает AudioTrack

---

## Фаза 10: Android — ViewModel и UI

---

### Module 16-android-viewmodel: CriViewModel

**Интерфейс:**
```kotlin
// CriViewModel.kt
data class CriViewState(
    val playbackState: PlaybackState,
    val segments: List<SubtitleSegment>,
    val activeWord: WordEntry?,
    val showPinyin: Boolean,
    val wordPopup: WordPopupState?,
    val connectionStatus: ConnectionStatus,
    val error: String?
)

data class WordPopupState(
    val word: WordEntry,
    val segment: SubtitleSegment,
    val pinyin: String,
    val translation: String
)

sealed class CriAction {
    data class Play(val serverUrl: String) : CriAction()
    object Pause : CriAction()
    object Resume : CriAction()
    data class SeekToWord(val word: WordEntry) : CriAction()
    data class WordTapped(val word: WordEntry) : CriAction()
    object DismissPopup : CriAction()
    object PronounceWord : CriAction()
    object SaveWord : CriAction()
    object TogglePinyin : CriAction()
}
```

**Приёмка:**
- [x] `TestInitialState` — IDLE, нет сегментов, нет ошибок
- [x] `TestPlayAction` — Play → LOADING → (mock player ready) → PLAYING
- [x] `TestPauseAction` — PLAYING + Pause → PAUSED
- [x] `TestWordTappedHighlighted` — PLAYING + WordTapped(highlighted word) → PAUSED + WordPopupState
- [x] `TestWordTappedNotHighlighted` — PLAYING + WordTapped(non-highlighted word) → PAUSED + activeWord меняется + WordPopupState
- [x] `TestSeekToWord` — SeekToWord → timeline сдвигается к началу слова
- [x] `TestTogglePinyin` — TogglePinyin → showPinyin инвертирован
- [x] `TestSaveWord` — SaveWord → vocabularyStore.appendWord вызван
- [x] `TestError` — player error → state.error не null, playbackState = ERROR

---

### Module 17-android-ui: Jetpack Compose UI

**Файлы:** `ui/CriApp.kt`, `ui/SubtitleArea.kt`, `ui/BottomControlBar.kt`, `ui/WordPopupDialog.kt`, `ui/SettingsDialog.kt`

**Приёмка (Compose Testing):**
- [x] `TestPlayButtonVisible` — в состоянии IDLE кнопка Play видна
- [x] `TestPauseButtonVisible` — в состоянии PLAYING кнопка Pause видна
- [x] `TestSubtitlesDisplayed` — segments не пуст → иероглифы отображаются
- [x] `TestPinyinDisplayed` — showPinyin=true → пиньинь над иероглифами
- [x] `TestPinyinHidden` — showPinyin=false → пиньиня нет
- [x] `TestActiveWordHighlighted` — activeWord не null → слово подсвечено amber
- [x] `TestWordPopupShow` — wordPopup не null → диалог с пиньинь и переводом
- [x] `TestWordPopupDismiss` — DismissPopup → диалог скрыт
- [x] `TestErrorDisplayed` — error не null → сообщение об ошибке видно
- [x] `TestSettingsDialog` — SettingsDialog → галочка showPinyin работает

---

## Фаза 11: Интеграция и финальная сборка

---

### Module 18-integration: End-to-end тесты (клиент + сервер)

**Приёмка:**
- [x] `TestClientServerHLS` — клиент play(serverUrl) → ExoPlayer загружает HLS → audio воспроизводится
- [x] `TestClientServerSSE` — клиент connect → получает sync → получает segments → segments отображаются
- [x] `TestClientServerSeek` — pause → seekToWord → resume → воспроизведение с новой позиции
- [x] `TestClientServerPauseResume` — pause на 30s → resume → воспроизведение продолжается
- [x] `TestClientServerPronounce` — word tapped → pronounce → AudioTrack проигрывает фрагмент
- [x] `TestClientServerSaveWord` — word tapped → save → слово в файле
- [x] `AndroidBuild` — `./gradlew assembleDebug` успешен
- [x] `AndroidTest` — `./gradlew test` все тесты зелёные
- [x] `AndroidLint` — `./gradlew lint` без критических ошибок

---

## Порядок реализации (dependency-ordered batches)

### Batch 0: Фундамент (1 разработчик, 1 день)
```
[00-models]        Go models + Kotlin models
[03-config]        Config + Logger (Go)
```

### Batch 1: Независимые модули (2-3 разработчика параллельно, 1-2 дня)
```
[01-tokenizer]     gse wrapper (Go)
[02-dictionary]    CC-CEDICT wrapper (Go)
[10-android-models] Kotlin models
[11-android-player] Media3 wrapper
[12-android-vocabulary] File store
```

### Batch 2: ASR + Storage (1-2 разработчика, 2-3 дня)
```
[04-asr]           whisper.cpp Go wrapper ← КРИТИЧЕСКИЙ МОДУЛЬ
[05-storage]       Metadata file store
```

### Batch 3: Ingest + Pipeline (1 разработчик, 1-2 дня)
```
[06-ingest]        ffmpeg wrapper
[07-pipeline]      Оркестрация
```

### Batch 4: API (1 разработчик, 1 день)
```
[08-api]           HTTP эндпоинты
[09-integration]   Server integration tests
```

### Batch 5: Android зависимые (1-2 разработчика, 2-3 дня)
```
[13-android-subtitles] SSE + cache
[14-android-sync]      Sync engine
[15-android-pronounce] Clip fetch + play
```

### Batch 6: Android финал (1 разработчик, 1-2 дня)
```
[16-android-viewmodel] CriViewModel
[17-android-ui]        Compose UI
[18-integration]       E2E tests
```

### Общая оценка: 10-15 дней (1-2 разработчика) или 5-8 дней (3-4 разработчика параллельно)

---

## Критические риски

| Риск | Вероятность | Влияние | Митигация |
|------|-------------|---------|-----------|
| whisper.cpp Go bindings не дают per-word тайминги | Средняя | Критическое | **Первый тест в 04-asr** — TestWhisperTimestamps. Если фейлится → ищем альтернативу (faster-whisper, sherpa-onnx align, MFA) |
| whisper large-v3 слишком медленный на CPU | Средняя | Высокое | Использовать `ggml-medium.bin` или `ggml-small.bin` + бенчмарк latency vs точность |
| HLS behind-live-window на Mobile | Низкая | Среднее | Media3 обрабатывает это сам. Тест TestPlayerBehindLiveWindow |
| SSE реконнект теряет сегменты | Средняя | Среднее | GET /metadata?start=&end= для докачки пропущенного |

---

## Структура репозитория

```
003_omc_cri2/
├── server/                     # Go server code
│   ├── cmd/server/main.go
│   ├── internal/
│   │   ├── models/models.go
│   │   ├── config/config.go
│   │   ├── logging/logging.go
│   │   ├── tokenizer/
│   │   │   ├── tokenizer.go      # interface
│   │   │   ├── gse_tokenizer.go  # impl
│   │   │   └── *_test.go
│   │   ├── dictionary/
│   │   │   ├── dictionary.go     # interface
│   │   │   ├── cedict_dict.go    # impl
│   │   │   └── *_test.go
│   │   ├── asr/
│   │   │   ├── asr.go            # interface
│   │   │   ├── whisper.go        # impl
│   │   │   └── *_test.go
│   │   ├── storage/
│   │   │   ├── storage.go        # interface
│   │   │   ├── fs_store.go       # impl
│   │   │   └── *_test.go
│   │   ├── ingest/
│   │   │   ├── ingest.go         # interface
│   │   │   ├── ffmpeg_ingest.go  # impl
│   │   │   └── *_test.go
│   │   ├── pipeline/
│   │   │   ├── pipeline.go       # interface
│   │   │   ├── runner.go         # impl
│   │   │   └── *_test.go
│   │   └── api/
│   │       ├── api.go            # router + handlers
│   │       └── *_test.go
│   ├── go.mod
│   ├── go.sum
│   ├── Dockerfile.base
│   ├── Dockerfile
│   ├── docker-build.sh
│   └── download-cache.sh
│
├── android/                     # Android client code
│   ├── app/src/main/java/com/crimobile/
│   │   ├── model/
│   │   ├── player/
│   │   ├── subtitles/
│   │   ├── sync/
│   │   ├── pronounce/
│   │   ├── vocabulary/
│   │   ├── viewmodel/
│   │   └── ui/
│   ├── app/src/test/java/com/crimobile/
│   └── build.gradle.kts
│
├── FINAL_ARCHITECTURE.md        # Этот документ
└── DEVELOPMENT_PLAN.md           # План разработки (этот)
```

---

## Итого: что делегировать агентам

Каждый Batch (0-6) — отдельная задача для агента. Внутри batch'а модули можно делать параллельно.

**Задача агента:**
1. Реализовать модуль(и) из batch'а согласно интерфейсам
2. Написать acceptance-тесты (все из списка приёмки)
3. Запустить тесты → все зелёные
4. Сообщить о завершении

**Порядок делегирования:**
1. Batch 0-1 (фундамент) → 2-3 агента параллельно
2. Batch 2 (ASR) → 1 агент (критический путь)
3. Batch 3 (Ingest+Pipeline) → 1 агент (после Batch 2)
4. Batch 4 (API) → 1 агент (после Batch 2-3)
5. Batch 5 (Android core) → 1-2 агента (после Batch 1 Android)
6. Batch 6 (Android UI + E2E) → 1 агент (после Batch 5)
