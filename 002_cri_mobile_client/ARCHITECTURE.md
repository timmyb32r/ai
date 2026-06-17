# CRI Mobile Client — Архитектура продукта

## 1. Описание продукта

**CRI Mobile Client** — Android-приложение для прослушивания прямого эфира «Международного радио Китая» (CRI-905) с синхронизированными китайскими субтитрами и поперсонажным amber-подсвечиванием в реальном времени.

### Какую проблему решает

Изучающие китайский язык получают инструмент, где они одновременно **слышат** живую китайскую речь, **видят** соответствующую расшифровку иероглифами, и **отслеживают** произносимое слово благодаря бегущему amber-подсвечиванию. Все субтитры генерируются полностью офлайн — сервер транскрибирует радио-поток через нейросеть SenseVoice без отправки данных во внешние API.

### Ключевые возможности

- **Живое радио CRI-905** — непрерывный аудиопоток через HTTP (PCM s16le 16kHz mono)
- **Субтитры в реальном времени** — китайские иероглифы с поперсонажным amber-подсвечиванием, синхронизированным со звуком
- **Пословный разбор** — тап по слову показывает пиньинь (романизацию), английский перевод, позволяет прослушать слово изолированно и сохранить в словарь
- **Пиньинь над иероглифами** — опциональное отображение романизации над каждым иероглифом
- **Фоновое воспроизведение** — Android foreground-сервис удерживает аудио даже при выключенном экране
- **Задержка 180 секунд** — трансляция идёт с настраиваемой задержкой относительно live-эфира, чтобы ASR успевал обрабатывать речь

---

## 2. Архитектура системы (общий обзор)

Система состоит из двух проектов:

```
┌──────────────────────────────────────────────────────────────────────┐
│                    001_omc_cri (Go-сервер)                            │
│                                                                        │
│  HLS URL ──► ffmpeg ──► PCM s16le ──► Buffer ──► ASR (SenseVoice)    │
│                              │                    │                    │
│                              │                    ▼                    │
│                              │              Enrich (CC-CEDICT)         │
│                              │                    │                    │
│                              ▼                    ▼                    │
│                      ┌──────────────────────────────────────┐        │
│                      │          HTTP API (:8080)             │        │
│                      │  /v1/stream/audio      (PCM chunked)  │        │
│                      │  /v1/stream/audio.opus (OGG/Opus)     │        │
│                      │  /v1/stream/subtitles  (SSE JSON)     │        │
│                      │  /v1/status            (JSON)         │        │
│                      └──────────┬───────────────────────────┘        │
└─────────────────────────────────┼────────────────────────────────────┘
                                  │ HTTP
                                  ▼
┌─────────────────────────────────┼────────────────────────────────────┐
│               002_cri_mobile_client (Android-клиент)                  │
│                                                                        │
│  ┌─────────────────────────────────────────┐                         │
│  │        Media3 ExoPlayer                  │                         │
│  │  (Opus/OGG → аппаратный декодер → PCM)   │                         │
│  │  Буфер / reconnect / AudioTrack — внутри  │                         │
│  └────────────────┬────────────────────────┘                         │
│                   │ PCM                                               │
│                   ▼                                                   │
│  ┌────────────────────────────┐    ┌──────────────────┐              │
│  │   PlaybackStateMachineImpl │    │ SseSubtitleSource │              │
│  │   (coroutine sync 10Hz,    │◄───│ (OkHttp SSE)      │              │
│  │    SSE-first ordering,     │    └────────┬─────────┘              │
│  │    player.currentPosition) │             │ SubtitleEvent            │
│  └──────┬──────────┬──────────┘             ▼                          │
│         │          │              ┌──────────────────┐                │
│         │          │              │  SyncedSegment   │                │
│         │          │              └────────┬─────────┘                │
│         │          │                       │                          │
│         │          ▼                       ▼                          │
│         │   ┌────────────────────────────────────┐                    │
│         │   │        CriViewModel                 │                    │
│         │   │  State, Segments, Highlight         │                    │
│         │   └────────────────┬───────────────────┘                    │
│         │                    │ Compose UI                              │
│         │                    ▼                                         │
│         │           ┌──────────────────┐                              │
│         │           │     CriApp       │                              │
│         │           │ (Jetpack Compose) │                              │
│         │           └──────────────────┘                              │
│         │                                                              │
│         ▼ (pronounce only)                                             │
│  ┌──────────────────┐                                                 │
│  │  PCMRingBuffer   │ ─── readRange() → MODE_STATIC AudioTrack        │
│  │  (60s PCM кэш)   │     (изолированное произношение слова)           │
│  └──────────────────┘                                                 │
└──────────────────────────────────────────────────────────────────────┘
```

---

## 3. Сервер (`001_omc_cri`)

Go-сервер выполняет четыре задачи: **ingest** (захват HLS), **буферизация**, **транскрибация** (ASR), и **fan-out** (раздача клиентам).

### 3.1. Пакетная структура

| Пакет | Назначение |
|-------|-----------|
| `cmd/server` | Точка входа. Парсит флаги, создаёт пайплайн, поднимает HTTP |
| `internal/ingest` | Запускает ffmpeg как подпроцесс, декодирует HLS в PCM s16le 16kHz mono |
| `internal/broadcast` | Ядро: кольцевой буфер (PCM + субтитры), драйвер ASR, pacer-ы подписчиков |
| `internal/api` | HTTP-поверхность: четыре эндпоинта |
| `internal/chineseasr` | Офлайн-транскрибация через sherpa-onnx-offline + SenseVoice |
| `internal/engine` | Низкоуровневая работа с sherpa-onnx: аргументы, парсинг вывода, детекция тишины |

### 3.2. Поток данных

```
HLS URL (sk.cri.cn/905.m3u8)
        │
        ▼
    [ffmpeg] ─── PCM s16le 16kHz mono ───► Buffer.AppendPCM()
        │                                          │
        │                                   ┌──────▼──────────┐
        │                                   │  Rolling Buffer   │
        │                                   │  (PCM + Subtitles)│
        │                                   └──────┬───────────┘
        │                                          │
        │                          ┌───────────────┼───────────────┐
        │                          │               │               │
        │                          ▼               ▼               ▼
        │                    Buffer.Read       Pacer (per        Pacer (per
        │                    ContiguousPCM     subscriber)      subscriber)
        │                    for ASR           releases PCM     releases Subs
        │                          │           at real-time     at real-time
        │                          ▼               │
        │              ┌─────────────────────┐      │
        │              │ ASR Driver (8s окна) │      │
        │              │ 1. PCM → temp WAV    │      │
        │              │ 2. sherpa-onnx-offline│     │
        │              │ 3. gse сегментация   │      │
        │              │ 4. CC-CEDICT обогащение│    │
        │              └──────────┬──────────┘      │
        │                         │                  │
        │                         ▼                  │
        │              Buffer.AppendSubtitle()       │
        │                                           │
        ▼                                           ▼
  ┌────────────────────────────────────────────────────┐
  │                  HTTP API (:8080)                   │
  │  GET /v1/stream/audio      → PCM s16le chunked     │
  │  GET /v1/stream/audio.opus → OGG/Opus 32kbps       │
  │  GET /v1/stream/subtitles  → SSE JSON              │
  │  GET /v1/status            → JSON                  │
  └────────────────────────────────────────────────────┘
```

### 3.3. Ключевые компоненты

**Ingestor** (`internal/ingest/ingest.go`)
- Запускает ffmpeg как подпроцесс: `ffmpeg -i <HLS_URL> -f s16le -acodec pcm_s16le -ac 1 -ar 16000 -`
- Автоматически переподключается при обрыве с экспоненциальным backoff (от 500ms до 30s)
- Поддерживает непрерывную временную шкалу (persistent byte clock) при переподключениях
- PCM bitrate: 256 kbps (s16le × 16kHz × 1 ch = 32 kB/s)

**Buffer** (`internal/broadcast/buffer.go`)
- Потокобезопасный кольцевой буфер с двумя дорожками: PCM и субтитры
- Временная шкала в секундах от начала ingest
- Автоматическое вытеснение (eviction) старых данных за пределы окна
- Методы: `AppendPCM`, `AppendSubtitle`, `ReadContiguousPCMFrom`, `ReadSubtitlesUpTo`, `ReadPCMFrom`

**ASR Driver** (`internal/broadcast/asr.go`)
- Забирает непрерывные PCM-фрагменты из буфера
- Работает окнами по 8 секунд с интервалом 2 секунды
- Для каждого окна: сохраняет PCM во временный WAV, запускает sherpa-onnx-offline, получает текст + per-token таймстемпы
- Сегментирует китайский текст на слова через gse (китайский сегментатор)
- Обогащает субтитры пиньинем и английским переводом через CC-CEDICT

**Lifecycle** (`internal/broadcast/lifecycle.go`)
- Автомат состояний с подсчётом ссылок: `Stopped → Starting → Warming → Running → Stopping → Stopped`
- Запускает ingest при первом подключении клиента
- Останавливает ingest через 10 секунд после отключения последнего клиента (linger)

**Pacer** (per-subscriber, в `broadcast.go`)
- На каждого подключившегося клиента создаётся горутина-pacer
- Выдаёт PCM (или Opus — через `SubscribeOpus`) и субтитры в real-time, отставая от live-края на `delay` (по умолчанию 180s)
- Субтитры привязаны к временной шкале через `start`/`end` SubtitleEvent и выдаются когда `broadcastHead ≥ subtitle.start`
- `Subscribe()` — PCM-подписчик (сырой PCM)
- `SubscribeOpus()` — Opus-подписчик: запускает per-subscriber ffmpeg `libopus 32kbps` для транскодирования PCM→OGG/Opus на лету

### 3.4. Зависимости сервера

| Зависимость | Назначение |
|-------------|-----------|
| `ffmpeg` | Декодирование HLS → PCM; опционально — транскодирование PCM→Opus |
| `sherpa-onnx-offline` | Нейросетевая транскрипция (SenseVoice) |
| SenseVoice model (`model.int8.onnx` + `tokens.txt`) | Модель для распознавания китайской речи |
| CC-CEDICT | Словарь для обогащения пиньинем и переводом |
| `go-ego/gse` | Сегментация китайского текста на слова |

---

## 4. Протокол взаимодействия

Клиент и сервер общаются по HTTP. Никакой аутентификации, WebSocket-ов, или HTTPS не требуется — всё внутри локальной сети.

### 4.1. Эндпоинты

#### `GET /v1/stream/audio` — Аудиопоток (PCM)

**Формат:** HTTP chunked transfer encoding
**Content-Type:** `audio/L16;rate=16000;channels=1`
**Данные:** Raw PCM s16le (16-bit signed integer, little-endian), 16000 Hz, 1 канал (моно)
**Битрейт:** 256 kbps (32 kB/s)

**Заголовки ответа:**
| Заголовок | Описание |
|-----------|----------|
| `X-Audio-Timeline-Start` | Абсолютная временная метка (в секундах) первого PCM-байта в этом соединении |
| `X-Audio-Bitrate` | Битрейт потока в kbps (256) |

Используется для: наполнения PCMRingBuffer (произношение слов). Основное аудио — через Opus-эндпоинт.

#### `GET /v1/stream/audio.opus` — Аудиопоток (Opus/OGG)

**Формат:** HTTP chunked transfer encoding
**Content-Type:** `audio/ogg; codecs=opus`
**Данные:** OGG-контейнер с Opus-кодеком, 32 kbps, моно
**Битрейт:** 32 kbps

**Заголовки ответа:**
| Заголовок | Описание |
|-----------|----------|
| `X-Audio-Timeline-Start` | Абсолютная временная метка первого аудио-байта |
| `X-Audio-Bitrate` | Битрейт потока в kbps (32) |

Используется для: основного аудио через Media3 ExoPlayer. Сервер транскодирует PCM→Opus per-subscriber через `ffmpeg -c:a libopus -b:a 32k`.

#### `GET /v1/stream/subtitles` — Поток субтитров

**Формат:** Server-Sent Events (SSE), `text/event-stream`
**Типы событий:**
| Event | Описание |
|-------|----------|
| `sync` | Отправляется первым. Содержит `AudioTimelineStart` — якорь временной шкалы |
| `subtitle` | Субтитр. JSON с `start`, `end`, `text_zh`, `words[]`, `pinyin`, `en` |
| `jump` | Сервер переподключился к HLS. Клиент должен переподключиться |

**Важно:** На клиенте timeline-якорь (`audioTimelineStartSec`) теперь читается из SSE `sync`-события (через `SseSubtitleSource.audioTimelineStartSec`), а не из HTTP-заголовка аудио-эндпоинта. Это универсальный источник — работает и с PCM, и с Opus.

#### `GET /v1/status` — Статус сервера

**Формат:** JSON
```json
{
  "channel": "https://sk.cri.cn/905.m3u8",
  "listeners": 1,
  "delaySeconds": 180,
  "state": "running",
  "liveEdgeOffsetSeconds": 175.2
}
```

### 4.2. Модель данных

**SubtitleEvent** (JSON через SSE):
```json
{
  "start": 123.45,
  "end": 127.89,
  "text_zh": "欢迎收听国际广播电台",
  "words": [
    {"char_start": 0, "char_end": 2, "start_sec": 0.0, "end_sec": 0.8, "pinyin": "huānyíng", "en": "welcome"},
    {"char_start": 2, "char_end": 4, "start_sec": 0.8, "end_sec": 1.2, "pinyin": "shōutīng", "en": "listen to"}
  ],
  "pinyin": "huānyíng shōutīng guójì guǎngbō diàntái",
  "en": "Welcome to listen to China Radio International"
}
```

**WordBoundary** — пословная разметка:
| Поле | Тип | Описание |
|------|-----|----------|
| `char_start` | int | Индекс первого символа слова в `text_zh` (0-based) |
| `char_end` | int | Индекс после последнего символа (exclusive) |
| `start_sec` | float | Начало слова относительно `start` субтитра |
| `end_sec` | float | Конец слова относительно `start` субтитра |
| `pinyin` | string | Пиньинь слова |
| `en` | string | Английский перевод слова |

### 4.3. Временная шкала (Timeline)

```
                    Live HLS edge
                         │
                         │ delay (180s)
                         ▼
              ┌─────────────────────┐
              │  Broadcast head     │ ← сервер отдаёт аудио/субтитры с этой позиции
              │  (live - delay)     │
              └─────────┬───────────┘
                        │
          ┌─────────────┼─────────────┐
          │             │             │
          ▼             ▼             ▼
     Pacer 1 (PCM)  Pacer 2 (Opus)  Pacer N
     (realtime)     (realtime +     (realtime)
                     ffmpeg transcode)
          │             │             │
          ▼             ▼             ▼
      Client N      Client N+1    Client N+2
```

- Сервер имеет единую временную шкалу от начала ingest (audio timeline), в секундах
- `AudioTimelineStart` из SSE `sync`-события синхронизирует клиента с этой шкалой
- Субтитры привязаны к шкале через `start`/`end` (абсолютные секунды)
- Pacer дозирует аудио в real-time относительно broadcast head
- Субтитры доставляются как только `broadcastHead ≥ subtitle.start`
- Для Opus-подписчиков: PCM→Opus транскодирование добавляет ~100-300ms задержки на ffmpeg pipeline

### 4.4. Точность синхронизации

- **Сервер:** задержка буфера + обработки ASR ≈ 180s от live
- **Клиент (Opus):** позиция из `ExoPlayer.currentPosition` (основана на `AudioTrack.getTimestamp()` — тот же аппаратный счётчик)
- **Клиент (PCM/pronounce):** `AudioTrack.playbackHeadPosition` для MODE_STATIC
- **Точность:** ±1.5 секунды (определяется размером окна ASR и джиттером сети)

---

## 5. Клиент (`002_cri_mobile_client`)

Android-приложение на Kotlin с Jetpack Compose UI.

### 5.1. Пакетная структура

```
com.crimobile/
├── api/                    # Интерфейсы (сервис-контракты)
│   ├── PlaybackStateMachine.kt   # Центральный координатор
│   ├── SubtitleStreamSource.kt   # Источник субтитров (SSE) + audioTimelineStartSec
│   └── HighlightTracker.kt       # Подсветка символов
│
├── service/                # Реализации интерфейсов
│   ├── PlaybackStateMachineImpl.kt  # Оркестратор: ExoPlayer + SSE sync
│   ├── SseSubtitleSource.kt         # OkHttp SSE-клиент + sync-якорь
│   └── HighlightTrackerImpl.kt      # Реализация подсветки
│
├── model/                  # Модели данных
│   ├── PlaybackState.kt       # IDLE, LOADING, PLAYING, PAUSED
│   ├── SubtitleEvent.kt       # Субтитр: start/end/textZh/words/pinyin/en
│   ├── SyncedSegment.kt       # Сегмент: PCM + SubtitleEvent
│   └── WordBoundary.kt        # Пословная разметка
│
├── infrastructure/         # Инфраструктура
│   ├── PCMRingBuffer.kt       # Кольцевой буфер PCM (60 сек) — pronounce
│   └── SubtitleParser.kt      # JSON-парсер субтитров (без аллокаций)
│
├── ui/                     # Jetpack Compose UI
│   └── CriApp.kt              # Все экраны в одном файле (~610 строк)
│
├── CriViewModel.kt         # ViewModel — состояние UI
├── PlaybackService.kt      # Foreground-сервис для фонового аудио
├── ServerConfig.kt         # SharedPreferences-конфиг (хост, порт, настройки)
└── MainActivity.kt         # Точка входа, DI вручную
```

**Удалённые файлы** (заменены Media3 ExoPlayer):
- ~~`AudioStreamSource.kt`~~ — интерфейс PCM-источника
- ~~`AudioOutput.kt`~~ — интерфейс аудиовыхода
- ~~`PcmAudioSource.kt`~~ — HTTP-читатель PCM
- ~~`AndroidAudioOutput.kt`~~ — AudioTrack-обёртка

### 5.2. Ключевые компоненты

#### PlaybackStateMachineImpl (`service/PlaybackStateMachineImpl.kt`)

Центральный оркестратор на базе **Media3 ExoPlayer** (вместо старого daemon-потока с кастомным AudioTrack).

**Архитектура:**
- **ExoPlayer** — основное аудио: Opus/OGG с `/v1/stream/audio.opus`. Управление буфером, декодированием и переподключением внутри Media3
- **SSE (SseSubtitleSource)** — субтитры + timeline-якорь (`audioTimelineStartSec` из `sync`-события)
- **PCMRingBuffer** — кольцевой буфер (60s) только для произношения слов. Наполняется фоновым подключением к `/v1/stream/audio` (PCM)
- **Coroutine sync loop** — 10 Hz вместо daemon-потока. Читает `player.currentPosition`, обновляет подсветку, дренирует субтитры

**Стратегия SSE-first ordering:**
1. `subtitleSource.connect()` — открыть SSE
2. `audioTimelineStartSec.filterNotNull().first()` — ждать sync-событие (с таймаутом 5s)
3. `exoPlayer.setMediaItem()` + `exoPlayer.prepare()` — только после получения якоря
4. `startSyncLoop()` — coroutine 10 Hz: дренаж субтитров, подсветка, статистика

**Синхронизация:**
- Позиция: `audioTimelineStartSec + exoPlayer.currentPosition / 1000.0`
- `currentPosition` основан на `AudioTrack.getTimestamp()` — тот же аппаратный счётчик, что использовался ранее
- Замена `playbackHeadPositionFrames / 16000.0` → `currentPosition / 1000.0`

**Потокобезопасность:**
- `mainScope` (`Dispatchers.Main`) — ExoPlayer (требует main thread) + sync loop
- `ioScope` (`Dispatchers.IO`) — фоновая PCM-подкачка для pronounce
- `MutableStateFlow` — для `audioTimelineStartSec` (пишет OkHttp callback, читает coroutine)

#### PCMRingBuffer (`infrastructure/PCMRingBuffer.kt`)

Потокобезопасный (`@Synchronized`) кольцевой буфер. **Больше не используется для основного воспроизведения** — только для pronounce.
- **Размер:** 1 920 000 байт = 60 секунд при 16kHz s16le mono
- **Запись:** `write(bytes)` — из фоновой PCM-подкачки (`/v1/stream/audio`)
- **Random access:** `readRange(timelineSec, lengthBytes)` — для `pronounceWord()`
- **Константы:** `SAMPLE_RATE=16000`, `BYTES_PER_SEC=32000`

#### SseSubtitleSource (`service/SseSubtitleSource.kt`)

SSE-клиент на базе OkHttp. Расширен относительно старой версии:
- `audioTimelineStartSec: StateFlow<Double?>` — потокобезопасный якорь временной шкалы из `sync`-события
- `sync`-событие теперь **устанавливает** `_audioTimelineStartSec.value` (не просто логирует)
- `hasJumpEvent` — флаг для переподключения (вызывает `reconnect()` в sync loop)
- `drainPending()` — атомарный дренаж накопленных SubtitleEvent
- Обрабатывает три типа событий: `sync`, `subtitle`, `jump`

#### SubtitleStreamSource (`api/SubtitleStreamSource.kt`)

Интерфейс расширен:
```kotlin
val audioTimelineStartSec: StateFlow<Double?>  // новое поле
```
Позволяет `PlaybackStateMachineImpl` ждать sync-событие перед запуском ExoPlayer.

#### HighlightTracker (`api/HighlightTracker.kt` + `service/HighlightTrackerImpl.kt`)

Без изменений относительно классической версии:
1. **Приоритет:** `findWordByTime()` — per-token таймстемпы из `words[]`
2. **Fallback:** `findWordRange()` — равномерное распределение символов
3. **Результат:** `IntRange` — `[highlightStart, highlightEnd)` для текущей позиции

### 5.3. UI-компоненты (`ui/CriApp.kt`)

| Компонент | Описание |
|-----------|----------|
| `CriApp` | Корневой Scaffold: top-bar (статус) + центр (субтитры) + низ (управление) |
| `ConnectionIndicator` | Ready / Buffering / Live / Paused — цвет + опциональный спиннер |
| `SubtitleArea` | LazyColumn с автоскроллом, последние 10 сегментов |
| `CharacterAlignedText` | Иероглифы с пиньинем над каждым символом (FlowRow) |
| `HanziText` | ClickableText с обработкой тапов по словам |
| `BottomControlBar` | Play/Pause по центру |
| `WordPopupDialog` | Пиньинь + перевод + Pronounce + Save |
| `SettingsDialog` | Хост/порт/пиньинь |
| `DebugLogView` | Отладочный лог |

### 5.4. Жизненный цикл

**PlaybackService** (`PlaybackService.kt`)
- Android foreground-сервис с `PARTIAL_WAKE_LOCK` (10 мин таймаут)
- Удерживает аудио при выключенном экране
- ExoPlayer управляет аудио-фокусом через `AudioAttributes` (USAGE_MEDIA, CONTENT_TYPE_SPEECH)

**CriViewModel** (`CriViewModel.kt`)
- `AndroidViewModel` — переживает поворот экрана
- Делегирует StateFlow от `PlaybackStateMachine` в UI
- `audioFlowing` — сохранён (теперь от ExoPlayer listener, не от PcmAudioSource)
- `delaySeconds` — опрос `/v1/status` каждые 5 секунд
- `WordPopupState` — показ/скрытие диалога слова
- `saveWord()` — MediaStore Downloads/cri-vocabulary.txt

**MainActivity** (`MainActivity.kt`)
- Ручной DI: создаёт `OkHttpClient`, `PCMRingBuffer`, `SseSubtitleSource`, `HighlightTrackerImpl`, `ExoPlayer`
- `ExoPlayer.Builder` с `AudioAttributes` (USAGE_MEDIA, SPEECH)
- `exoPlayer.release()` в `onDestroy()` — предотвращает утечку нативных ресурсов

### 5.5. Технологический стек

| Компонент | Технология |
|-----------|-----------|
| Язык | Kotlin 1.9.22 |
| UI | Jetpack Compose BOM 2024.04.00, Material 3 |
| Аудио (основное) | **AndroidX Media3 ExoPlayer 1.4.1** (Opus/OGG) |
| Аудио (pronounce) | `android.media.AudioTrack` (MODE_STATIC, PCM) |
| HTTP | OkHttp 4.12.0 |
| Concurrency | Kotlinx Coroutines, `StateFlow` |
| DI | Ручной (через `ViewModelProvider.Factory`) |
| Build | Gradle 8.2.2, AGP 8.2.2 |
| minSdk / targetSdk | 26 / 34 |

### 5.6. Поток данных при воспроизведении

```
1. СТАРТ
   MainActivity → CriViewModel.play()
   → PlaybackStateMachineImpl.play()
   → PlaybackState: IDLE → LOADING

2. SSE-FIRST ORDERING
   ├─ SseSubtitleSource.connect(baseUrl)
   │  └─ OkHttp → /v1/stream/subtitles
   │     └─ SSE callback → sync event → audioTimelineStartSec = 0.0
   │
   ├─ Ждать audioTimelineStartSec.filterNotNull().first() (timeout 5s)
   │
   ├─ ExoPlayer: MediaItem.fromUri("/v1/stream/audio.opus")
   │  └─ exoPlayer.prepare() → Opus декодер → AudioTrack
   │
   └─ startSyncLoop() — coroutine 10Hz

3. PLAYING
   PlaybackState: LOADING → PLAYING (on Player.STATE_READY)

4. ЦИКЛ СИНХРОНИЗАЦИИ (10 Hz, Dispatchers.Main)
   ├─ drainPending() — субтитры из SSE
   ├─ hasJumpEvent? → reconnect()
   ├─ player.currentPosition → позиция для подсветки
   ├─ highlightTracker.update(sub, pos) → [highlightStart, highlightEnd)
   ├─ Каждые 500ms: _segmentsFlow.value = последние 10 сегментов
   └─ Каждые 10s: лог статистики

5. ФОНОВАЯ PCM-ПОДКАЧКА (Dispatchers.IO)
   └─ HttpURLConnection → /v1/stream/audio (PCM)
      └─ pcmBuffer.write(pcm) — наполнение для pronounce

6. ПРОИЗНОШЕНИЕ СЛОВА
   CriApp.onWordClick(word)
   → pause → WordPopupDialog
   → Pronounce: pcmBuffer.readRange(timelineSec, bytes)
     → AudioTrack(MODE_STATIC) → play → sleep → release
   → Save: MediaStore.Downloads/cri-vocabulary.txt
```

---

## 6. Ключевые архитектурные решения

### 6.1. PCM на сервере, Opus для клиента
Сервер хранит PCM s16le (для ASR) и транскодирует в Opus/OGG (32 kbps libopus) per-subscriber для клиентов. Два параллельных эндпоинта:
- `/v1/stream/audio` (PCM) — для pronounce (клиент) и обратной совместимости
- `/v1/stream/audio.opus` (OGG/Opus) — для основного аудио через ExoPlayer

### 6.2. Media3 ExoPlayer вместо кастомного AudioTrack
Основное аудио передано ExoPlayer. Это устраняет ручное управление буфером, underrun-ы из-за `@Synchronized` contention, и ручную логику переподключения. ExoPlayer использует аппаратный Opus-декодер (`c2.android.opus.decoder`).

### 6.3. SSE-first ordering
Порядок старта: SSE connect → ждать sync-событие → ExoPlayer.prepare(). Timeline-якорь гарантированно доступен до начала аудио.

### 6.4. PCMRingBuffer только для pronounce
Кольцевой буфер сохранён исключительно для изолированного произношения слов (`readRange()` + `MODE_STATIC AudioTrack`). Наполняется фоновым подключением к PCM-эндпоинту.

### 6.5. Офлайн ASR
Транскрибация выполняется полностью локально через sherpa-onnx + SenseVoice. Никакие аудиоданные не покидают сервер.

### 6.6. Задержка 180 секунд
Буфер в 3 минуты между live-краем HLS и broadcast head даёт ASR время на обработку 8-секундных окон.

### 6.7. Ref-counted жизненный цикл
Сервер запускает ingest при первом подключении и останавливает через 10 секунд после ухода последнего клиента.

---

## 7. Сборка и запуск

### Сервер
```bash
cd 001_omc_cri
go run ./cmd/server \
  -model-dir /path/to/sherpa-onnx-sense-voice-zh-en-ja-ko-yue-2024-07-17 \
  -delay 180s
```

### Клиент
```bash
cd 002_cri_mobile_client
./gradlew assembleDebug
# Установить APK на устройство/эмулятор
```

### Тесты
```bash
# Сервер
cd 001_omc_cri && go test ./...

# Клиент
cd 002_cri_mobile_client && ./gradlew test
```

---

## 8. Ссылки

- [Серверный README](../001_omc_cri/README-radio.md) — детальная документация сервера
- [CLAUDE.md](./CLAUDE.md) — инструкции для AI-агентов
- [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) — движок офлайн-транскрибации
- [SenseVoice](https://github.com/FunAudioLLM/SenseVoice) — модель распознавания речи
- [AndroidX Media3](https://developer.android.com/guide/topics/media/media3) — ExoPlayer
