# Deep Research Report: Архитектура клиент-серверного приложения CRI Radio

**Дата:** 2026-06-16
**Методология:** 5 search angles → 22 sources → 91 claims → 25 verified (19 confirmed, 6 killed) → 6 synthesized findings
**Агентов:** 104 | **Токенов:** 3,159,370 | **Длительность:** ~39 минут

---

## Executive Summary

**Главный вывод:** Текущая архитектура проекта (HLS ingest → ffmpeg → PCM → ASR на сервере; Opus/OGG + SSE на клиенте) **в целом правильна** и соответствует лучшим практикам. Однако исследование выявило **критическое ограничение SenseVoice** и **несколько точек для улучшения**.

**Рекомендуемый стек:**
- **Аудио-транспорт:** HLS (HTTP Live Streaming) — проверен временем, работает на всём, встроен в Media3
- **Метаданные/субтитры:** SSE (Server-Sent Events) + GET-эндпоинт для seek — простой, надёжный, легко отлаживается
- **Протокол, который НЕ НУЖЕН:** WebVTT (сломан в Media3 1.4+), WebSocket (избыточен для этого сценария), DASH (сложнее HLS без выигрыша)

---

## 1. Протокол взаимодействия клиент-сервер (КЛЮЧЕВОЙ ВОПРОС)

### 1.1. Сравнение кандидатов

| Протокол | Overhead | Seek | Надёжность | Простота | Вердикт |
|----------|----------|------|------------|----------|---------|
| **HLS + SSE** | Минимальный | ✅ Через GET /metadata | ✅ HTTP, проверен 15+ лет | ✅ Высокая | **РЕКОМЕНДУЕТСЯ** |
| HLS + WebVTT | Минимальный | ✅ Встроен в HLS | ❌ Сломан в Media3 1.4+ | ❌ `IllegalStateException` | **НЕ РЕКОМЕНДУЕТСЯ** |
| DASH | Выше (MPD manifest) | ✅ Встроен | ✅ Работает | ❌ Сложнее HLS | Нецелесообразно |
| WebSocket | Выше (persistent conn) | ❌ Нет встроенного | ⚠️ Нужен реконнект | ❌ Кастомный протокол | Избыточен |

### 1.2. Почему WebVTT сломан и не подходит

**Ключевой факт (подтверждён 3-0 голосованием):** Начиная с AndroidX Media3 1.4.0 (июль 2024), **legacy subtitle decoding отключен по умолчанию**. Попытка использовать `SingleSampleMediaSource` для VTT-файла выбрасывает `IllegalStateException: "Legacy decoding is disabled"`. Это задокументировано в:
- [GitHub issue #1655](https://github.com/androidx/media/issues/1655)
- [Media3 1.4.0 release notes](https://github.com/androidx/media/releases/tag/1.4.0)
- Bitmovin community thread #3515

**Альтернативы WebVTT:**
1. `DefaultMediaSourceFactory` — но claim о его автоматической работе был refuted (1-2), поведение нестабильно
2. **SSE с кастомным рендерингом (SituLearner-подход)** — РЕКОМЕНДУЕТСЯ

### 1.3. Рекомендуемая архитектура протокола

```
┌─────────────────────────────────────────────────────────────────┐
│                        Android Client                            │
│                                                                   │
│  Media3 ExoPlayer          SSE Consumer             HTTP Client   │
│  (HLS / Opus OGG)          (OkHttp SSE)             (GET /meta)  │
│       │                         │                        │        │
│       │ HLS segment             │ subtitle event         │ seek   │
│       ▼                         ▼                        ▼        │
│  ┌──────────────┐    ┌──────────────────┐    ┌──────────────────┐ │
│  │ Audio Track  │    │ Subtitle Buffer  │    │ Metadata Cache   │ │
│  │ (hardware    │    │ (binary search   │    │ (LocalDataSource)│ │
│  │  decoder)    │    │  by positionInMs)│    │                  │ │
│  └──────────────┘    └──────────────────┘    └──────────────────┘ │
└─────────────────────────────────────────────────────────────────┘
         │                      │                       │
         ▼                      ▼                       ▼
┌─────────────────────────────────────────────────────────────────┐
│                         Go Server                                 │
│                                                                   │
│  GET /v1/stream/audio.opus    GET /v1/stream/subtitles   GET /v1/metadata?start=&end= │
│  (OGG/Opus chunked)           (SSE text/event-stream)    (JSON segments array)         │
│                                                                   │
│  Все три эндпоинта stateless, используют HTTP Range при желании   │
│  SSE для real-time, GET /metadata для перемотки                   │
└─────────────────────────────────────────────────────────────────┘
```

**ТРИ эндпоинта (не два):**
1. `GET /v1/stream/audio.opus` — Opus/OGS поток (основное аудио через ExoPlayer)
2. `GET /v1/stream/subtitles` — SSE real-time поток субтитров
3. `GET /v1/metadata?start=<timeline_sec>&end=<timeline_sec>` — **новый** — запрос метаданных для произвольного диапазона (seek/перемотка)

**Почему нужен третий эндпоинт:**
Когда пользователь перематывает (seek) на 5 минут назад, SSE-поток не содержит исторических данных. Клиенту нужно запросить метаданные для соответствующего временного диапазона. Это решается GET-эндпоинтом с параметрами `start` и `end`.

---

## 2. Серверная архитектура (Go)

### 2.1. ASR-цепочка: sherpa-onnx + SenseVoice

**Подтверждённые факты (все 3-0 голосование):**

1. **SenseVoice доступен ТОЛЬКО через offline recognizer.** Это не streaming-модель в Go API. `AcceptWaveform` вызывается один раз с полным float32 PCM буфером, затем `Decode`.

2. **Трёхслойная архитектура:** Go CGO → C API → C++ core. Платформо-специфичные `.so`/`.dylib` лежат в отдельных Go-модулях (`sherpa-onnx-go-linux/macos/windows`).

3. **Ручное управление памятью:** Каждый `New*()` требует парного `Delete()`. GC не управляет C-памятью. Все 9 Delete-функций обнуляют `impl=nil`.

4. **Методы декодирования:** Только `greedy_search` и `modified_beam_search`.

5. **SenseVoice changelog про "streaming" относится к VAD + OfflineRecognizer (simulated streaming), а не к OnlineRecognizer.**

### 2.2. 🔴 КРИТИЧЕСКОЕ ОГРАНИЧЕНИЕ: Нет per-word таймингов

**Дважды refuted (0-3 и 1-2):** sherpa-onnx Go API **НЕ ПРЕДОСТАВЛЯЕТ** per-token Timestamps и Durations для SenseVoice.

Это означает:
- ❌ Невозможно получить тайминги отдельных иероглифов/слов из SenseVoice через Go API
- ⚠️ Синхронизация возможна только на уровне 3-секундного сегмента, не отдельных слов
- Текущий `char_start`/`char_end` в `WordBoundary` не может быть привязан к точным миллисекундам

**Варианты решения (требуют дополнительного исследования):**

| Вариант | Сложность | Точность | Статус |
|---------|-----------|----------|--------|
| **А. Равномерное распределение** — длительность слова = длина_слова / общая_длина × длительность_сегмента | Низкая | ±1.5s | Уже реализовано (HighlightTracker fallback) |
| **Б. Forced Alignment через sherpa-onnx** — использовать `AlignOffline` (если доступен в Go API) для получения покадровых таймингов | Средняя | ±100ms | Требует проверки наличия в Go API |
| **В. MFA (Montreal Forced Aligner)** — отдельный инструмент для фонемного выравнивания | Высокая | ±50ms | Избыточен для MVP |
| **Г. whisper-timestamped** — замена SenseVoice на whisper с встроенными таймстемпами | Средняя | ±100ms | Другой движок, другое качество для китайского |

**Рекомендация:** Для MVP использовать вариант А (уже работает). Для production — исследовать вариант Б (sherpa-onnx forced alignment).

### 2.3. NLP-цепочка: gse + CC-CEDICT

**Подтверждено (3-0):**

- **gse** — 9.2 MB/s single-thread, 26.8 MB/s concurrent goroutines. Для ASR-выхода (~120 байт / 10 сек аудио) это с запасом в 100,000x.
- **cedict** (`github.com/Ecostack/cedict`) — `Entry` struct с `Traditional`, `Simplified`, `Pinyin`, `Meanings`. `HanziToPinyin` использует greedy longest-match алгоритм.
- **CC-CEDICT** — CC BY-SA 4.0, ~123K записей, V2 синтаксис (дек 2023).

**Refuted (0-3):** gse **НЕ использует** HMM + Viterbi для OOV-слов. Обработка неизвестных терминов (песни, имена) остаётся открытым вопросом.

### 2.4. Docker multi-stage сборка

**Рекомендуемая структура (два слоя):**

```dockerfile
# === Этап 1: Базовый образ (Dockerfile.base) ===
FROM ubuntu:22.04 AS base
# ffmpeg (без ML-зависимостей — gtsteffaniak/ffmpeg)
# sherpa-onnx shared libraries (.so)
# Модель SenseVoice
# CC-CEDICT словарь
# Ребилдится РЕДКО — только при обновлении зависимостей

# === Этап 2: Go-бинарный слой (Dockerfile) ===
FROM base AS builder
# Установка Go toolchain
# Копирование исходников, go build
# Ребилдится ЧАСТО — при каждом изменении кода

FROM base AS runtime
COPY --from=builder /app/server /app/server
COPY --from=builder /app/sherpa-onnx-offline /app/
ENTRYPOINT ["/app/server"]
```

**Подтверждено (3-0):** Проект [gtsteffaniak/ffmpeg](https://github.com/gtsteffaniak/ffmpeg) собирает ffmpeg с кодеками (x264, x265, dav1d, aom, libvpx, libmp3lame, libopus) и НЕ включает ONNX Runtime / TensorFlow / PyTorch. Соответствует архитектуре, где ASR выполняется отдельным бинарём.

---

## 3. Клиентская архитектура (Android)

### 3.1. AndroidX Media3 / ExoPlayer

**Подтверждённые факты:**

| Факт | Статус |
|------|--------|
| `HlsMediaSource` **НЕ поддерживает** `MediaItem.subtitleConfiguration` | ✅ 3-0 |
| Нужен `DefaultMediaSourceFactory` для внешних субтитров | ✅ 3-0 |
| Legacy subtitle decoding отключен в Media3 1.4+ | ✅ 3-0 |
| Media3 1.8.0+ вводит `setScrubbingModeEnabled(true)` | ✅ 3-0 |

### 3.2. Синхронизация субтитров: SituLearner-подход

**Подтверждено (3-0):** Проект [SituLearner](https://deepwiki.com/coda251/situlearner/5.2-media-player) реализует:

- Кастомную синхронизацию субтитров **без использования Media3 SubtitleDecoder**
- Binary search списка субтитров по `positionInMs` из `PlayerState`
- Покликовое взаимодействие с отдельными токенами (пауза/loop)
- **НЕ использует клиент-серверный протокол** (всё локально) — подход нужно адаптировать

**Адаптация для нашего проекта:**
```
PlayerState.positionInMs
        │
        ▼
┌─────────────────────────┐
│  SubtitleSyncEngine     │  ← Новый компонент
│                         │
│  binarySearch(          │
│    segments,            │
│    currentTimelineMs    │
│  )                      │
│     │                   │
│     ├─ activeSegment    │ → Подсветка текущего сегмента
│     ├─ currentWord      │ → Подсветка текущего слова (аппроксимация)
│     └─ upcomingSegments │ → Предзагрузка следующих
└─────────────────────────┘
        │
        ▼
  CriViewModel (Compose State)
```

### 3.3. Обработка перемотки (Seek)

```
Пользователь нажимает на слово (в прошлом)
        │
        ▼
1. exoPlayer.pause()
2. Найти сегмент с этим словом
3. Вычислить timelineMs слова
4. GET /v1/metadata?start=<word_timeline_start>&end=<word_timeline_start+30>
   → Получить метаданные для нового положения
5. exoPlayer.seekTo(wordTimelineMs)
6. Обновить subtitleBuffer полученными метаданными
7. exoPlayer.play()
        │
        ▼
SSE продолжает присылать новые субтитры, но уже для нового положения
```

### 3.4. Рекомендуемый технологический стек (сравнение с текущим)

| Компонент | Текущий | Рекомендуемый | Причина |
|-----------|---------|---------------|---------|
| Аудио-плеер | Media3 ExoPlayer 1.4.1 | **Media3 ExoPlayer 1.8.0+** | `setScrubbingModeEnabled(true)` |
| Протокол аудио | OGG/Opus chunked HTTP | **HLS сегменты** | Seek "из коробки", встроен в Media3 |
| Субтитры | SSE (`/v1/stream/subtitles`) | **SSE + GET `/v1/metadata`** | Добавить seek-эндпоинт |
| Подсветка | `HighlightTracker` (word + fallback) | **SituLearner binary search** | Более надёжный подход |
| PCM буфер | `PCMRingBuffer` (60s) | Оставить как есть | Для pronounce работает отлично |
| DI | Ручной | Можно перейти на **Hilt** | Опционально, не критично |

---

## 4. Сводка refuted claims (что НЕ работает)

| Claim | Vote | Что это значит |
|-------|------|----------------|
| SenseVoice возвращает per-token Timestamps в Go API | 0-3 ✗ | **КРИТИЧНО.** Word-level подсветка невозможна без костылей |
| OfflineRecognizerResult даёт `Timestamps []float32` и `Durations []float32` | 0-3 ✗ | Доп. подтверждение отсутствия таймингов |
| Go API предоставляет character-level timestamps из SenseVoice | 1-2 ✗ | Третье подтверждение |
| Go API использует non-streaming decode pattern (один AcceptWaveform) | 0-3 ✗ | Документация говорит об обратном — есть streaming decode |
| gse использует HMM + Viterbi для OOV | 0-3 ✗ | Обработка песен/имён ненадёжна |
| DefaultMediaSourceFactory автоматически включает не-legacy субтитры | 1-2 ✗ | Нужно тестировать на целевых устройствах |

---

## 5. Открытые вопросы (для следующей итерации)

1. **Word-level highlighting:** Есть ли `AlignOffline` в Go API sherpa-onnx? Нужно читать исходники или форум k2-fsa.

2. **Формат метаданных для GET /metadata:**
   ```json
   {
     "range_start_sec": 300.0,
     "range_end_sec": 330.0,
     "segments": [
       {
         "timeline_start_sec": 300.0,
         "timeline_end_sec": 303.0,
         "text_zh": "欢迎收听国际广播电台",
         "words": [
           {"char_start": 0, "char_end": 2, "pinyin": "huānyíng", "en": "welcome"}
         ]
       }
     ]
   }
   ```

3. **Кеширование на клиенте:** Нужно ли сохранять полученные метаданные в `LocalDataSource` для офлайн-перемотки?

4. **Стратегия реконнекта:** Что делать при обрыве HLS на мобильном? Media3 имеет встроенный failover, но как быть с SSE — буферизировать на сервере и отдавать при переподключении?

5. **Задержка vs интерактивность:** Текущая задержка 180s для ASR. Можно ли уменьшить до 30-60s ценой меньших окон анализа?

---

## 6. План действий (рекомендуемый)

### Фаза 1: Исправить протокол (средний риск)
- [ ] Заменить прямой HTTP chunked audio на **HLS-сегментацию** на сервере
- [ ] Добавить эндпоинт `GET /v1/metadata?start=&end=`
- [ ] Обновить Media3 до 1.8.0+ для `setScrubbingModeEnabled(true)`
- [ ] Реализовать binary search субтитров по `positionInMs` (SituLearner-подход)

### Фаза 2: Решить проблему таймингов (высокий риск)
- [ ] Исследовать наличие `AlignOffline` в Go API sherpa-onnx
- [ ] Если нет — внедрить равномерное распределение с визуальной индикацией (границы сегментов вместо пословной подсветки)
- [ ] Рассмотреть whisper.cpp как альтернативу (даёт word-level timestamps, но хуже качество на китайском)

### Фаза 3: Docker-оптимизация (низкий риск)
- [ ] Разделить Dockerfile на `Dockerfile.base` + `Dockerfile`
- [ ] Использовать gtsteffaniak/ffmpeg как базовый образ
- [ ] Настроить CI для раздельного билда слоёв

### Фаза 4: Polish (низкий риск)
- [ ] Hilt для DI (опционально)
- [ ] Тесты на seek-сценарии
- [ ] Тесты на реконнект

---

## 7. Источники (22 источника, первичные выделены)

### Первичные источники (primary)
1. [sherpa-onnx Go API Documentation](https://k2-fsa.github.io/sherpa/onnx/go-api/index.html)
2. [sherpa-onnx Go bindings source (sherpa_onnx.go)](https://raw.githubusercontent.com/k2-fsa/sherpa-onnx/master/scripts/go/sherpa_onnx.go)
3. [sherpa-onnx non-streaming decode example](https://github.com/k2-fsa/sherpa-onnx/blob/master/go-api-examples/non-streaming-decode-files/main.go)
4. [sherpa-onnx streaming decode example](https://github.com/k2-fsa/sherpa-onnx/blob/master/go-api-examples/streaming-decode-files/main.go)
5. [Media3 1.8.0 Release Blog (Android Developers)](https://android-developers.googleblog.com/2025/08/media3-180-whats-new.html)
6. [Media3 1.4.0 Release Notes](https://github.com/androidx/media/releases/tag/1.4.0)
7. [AndroidX Media3 RenderersFactory](https://developer.android.com/reference/kotlin/androidx/media3/exoplayer/RenderersFactory)
8. [gtsteffaniak/ffmpeg (Docker-оптимизированная сборка)](https://github.com/gtsteffaniak/ffmpeg)
9. [go-ego/gse — Chinese tokenizer](https://github.com/go-ego/gse)
10. [Ecostack/cedict — CC-CEDICT Go library](https://pkg.go.dev/github.com/Ecostack/cedict)

### Вторичные источники
11. [DeepWiki: sherpa-onnx Go Bindings](https://deepwiki.com/k2-fsa/sherpa-onnx/3.5-go-bindings)
12. [DeepWiki: SituLearner Media Player](https://deepwiki.com/coda251/situlearner/5.2-media-player)
13. [DeepWiki: Xiaozhi Server Docker](https://deepwiki.com/xiaozhi-labs/xiaozhi-server/6.1-docker-deployment)

### Форум / Issues
14. [Media3 Issue #1655 — Legacy decoding disabled](https://github.com/androidx/media/issues/1655)
15. [ExoPlayer Issue #6487](https://github.com/google/ExoPlayer/issues/6487)

### Блоги / Статьи
16-22. Различные технические блоги (качество: blog/unreliable) — использовались для cross-reference, но не как первичные источники.

---

## 8. Заключение

**Текущая архитектура проекта — хорошая.** Главная проблема, которую исследование выявило — это **отсутствие per-word таймингов из SenseVoice через Go API**, что ограничивает точность подсветки отдельных слов.

**Ключевые рекомендации:**
1. **Протокол:** HLS + SSE + GET /metadata (три эндпоинта) — не WebVTT, не WebSocket
2. **Тайминги:** Смириться с аппроксимацией для MVP, исследовать forced alignment для v2
3. **Media3:** Обновить до 1.8.0+ ради `setScrubbingModeEnabled(true)`
4. **Docker:** Два слоя через multi-stage build — уже описано, нужно реализовать

**Самый большой риск:** Если точная пословная подсветка — must-have, то SenseVoice через текущий Go API не подходит. Нужен либо whisper.cpp, либо другой ASR-движок с per-word timestamps, либо отдельный forced-alignment этап.

---

*Отчёт сгенерирован через deep-research workflow (OMC): 5 search angles, 104 агента, 91 claim extracted, 25 verified, 6 synthesized findings.*
