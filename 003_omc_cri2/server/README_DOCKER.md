# CRI Radio Server — Docker

Три команды со свежего клона до работающего контейнера:

```bash
# 1. Скачать модель + словари
./download-cache.sh sense-voice-2024

# 2. Собрать образ
./docker-build.sh --rebuild-base

# 3. Запустить (локально)
docker-compose up
```

Для сервера — вместо `docker-compose up`:

```bash
docker run -d \
  --name criradio \
  --restart unless-stopped \
  -p 8080:8080 \
  -e CHANNEL_URL=https://sk.cri.cn/905.m3u8 \
  -v criradio_data:/tmp/china_radio_international \
  criradio-server:latest
```

`-d` — демон в фоне, `--restart unless-stopped` — перезапуск при падении и после ребута.

---

## Модели

`sense-voice-2024` — SenseVoice Small (sherpa-onnx, zh/en/ja/ko/yue, ~140MB).  
Другие: `ggml-small`, `ggml-large` (whisper.cpp), `paraformer-zh`, `sherpa-whisper-small`.

Смена модели:
```bash
./download-cache.sh ggml-large
./docker-build.sh --rebuild-base
```

## Порты

| Порт | Назначение |
|------|-----------|
| 8080 | HTTP API + HLS + SSE |
| 6060 | pprof (диагностика) |

## Env

| Переменная | По умолчанию | Описание |
|-----------|-------------|----------|
| `CHANNEL_URL` | `https://sk.cri.cn/905.m3u8` | URL радио-потока |
| `ASR_ENGINE` | из `.build-config` | `whisper` или `sherpa-onnx` |
| `ASR_MODEL` | из `.build-config` | short-name модели |
| `MODEL_PATH` | `/opt/models` | путь к файлам модели |
| `DELAY` | `180s` | задержка от live edge |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
