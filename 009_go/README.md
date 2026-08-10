# transferia2-go

Высокопроизводительное подмножество `005_rust`: только доставка
`PQv1 (Logbroker/YDS) → Apache Arrow → ClickHouse`.

Программа принимает тот же YAML и те же обязательные CLI-флаги:

```bash
go build -trimpath -pgo=auto -ldflags='-s -w' -o bin/transferia ./cmd/transferia
./bin/transferia \
  --config benchmarks/config_bench_yds_json_parser_to_ch.yaml \
  --total-workers 1 \
  --worker-index 0
```

Перед запуском заполните в YAML `source.pqv1.connection_string`,
`sink.clickhouse.connection_string`, пароль и token/token_file. Как и Rust-версия,
этот benchmark-конфиг с пустыми секретами сам по себе не подключится.

## Что оптимизировано

- Сетевой `Read` PQv1 отправляется до декомпрессии текущего batch, поэтому
  download перекрывается с decode/parse/insert.
- Фиксированный worker pool параллельно выполняет gzip/zstd decode и JSON parse;
  результаты восстанавливаются в исходном порядке перед commit.
- Специализированный однопроходный JSON scanner извлекает все root-поля без
  `map[string]any`, reflection и отдельного DOM.
- Parser workspace, zstd decoder, gzip reader и decompression buffers
  переиспользуются каждым worker.
- Arrow является настоящим промежуточным форматом. ClickHouse Native adapters
  кодируют строки прямо из Arrow offsets/data, а Int32/Int64 — прямо из Arrow
  value slices. Обратной материализации в строки/строки таблицы нет.
- `ch-go` пишет несколько Arrow record batches блоками одного INSERT и использует
  LZ4 на wire.
- PQv1 cookie подтверждается только после успешной записи main и DLQ batches в
  ClickHouse. Гарантия — at-least-once.

Оптимизированный parser намеренно поддерживает типы, используемые данным YAML:
`Utf8`/`String` и `Int32`. JSONPath должен быть простым root-полем (`$.id`).
Невалидные JSON-строки и строки без required-полей попадают в `<table>_dlq`.

## Проверки и benchmark

```bash
make test
make bench
make race
```

На Apple M3 Pro parser benchmark формы из YAML (1000 строк, 12 колонок):

```text
~415 MB/s, 174 allocs на batch = 0.174 alloc/row
```

Это microbenchmark parser-а, не обещание end-to-end throughput: итог ограничат
размер/codec сообщений, сеть YDS, схема и latency ClickHouse.

Для профилирования доступны:

```bash
./bin/transferia --config config.yaml --cpuprofile cpu.pprof --memprofile heap.pprof
go tool pprof -http=:8080 cpu.pprof
make build PGO=cpu.pprof
```

Полезные production-настройки после измерения на реальной нагрузке:

```bash
GOMAXPROCS=12 GOGC=200 ./bin/transferia --config config.yaml
```

`--pipeline-workers 0` автоматически делит `GOMAXPROCS` между назначенными
partition (не более 8 decode+parse workers на partition). Явное значение удобно
подбирать вместе с `batch_size`, `max_linger_ms` и `max_connections`.

## Protobuf

В репозитории лежит минимальный wire-совместимый PQv1/Discovery proto и
сгенерированный Go-код. Перегенерация:

```bash
PATH="$(go env GOPATH)/bin:$PATH" go generate ./...
```
