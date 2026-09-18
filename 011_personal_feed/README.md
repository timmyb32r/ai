# Personal Feed

Go-сервис → SQLite → 24 независимые RSS-ленты для Inoreader. Девять источников с готовыми RSS/Atom исключены из конфигурации для прямой подписки в Inoreader. Источники: RSS/Atom, HTML с CSS-селекторами, Chromium для JavaScript и адаптер официального API Cloudera.

## Запуск

На сервере нужны Docker Engine и Docker Compose:

```sh
docker compose up -d --build
docker compose logs -f --tail=100
```

Если установлен отдельный Compose, используйте `docker-compose` вместо `docker compose`.

Откройте `http://IP-СЕРВЕРА:8080/`: там список источников, ссылки RSS, последние успешные проверки и ошибки. При первом запуске загрузка идёт в фоне и занимает несколько минут; блокировки сайтов могут увеличить время. До первого успешного обхода конкретная лента отвечает 503 с Retry-After.

- `/databricks.xml`, `/claude.xml`, `/cloudera.xml` и остальные `/<id>.xml` — RSS.
- `/opml.xml` — импорт всех подписок в Inoreader через OPML.
- `/status.json` — состояние источников.
- `/healthz` — работоспособность сервиса и базы. Ошибка отдельного сайта не делает весь сервис unhealthy.

`PORT=8090 docker compose up -d` меняет внешний порт. DNS контейнера по умолчанию — `1.1.1.1`; другой сервер задаётся через `DNS_SERVER=адрес docker compose up -d`. Это устраняет обнаруженную при тестировании проблему DNS `claude.com` у локального Docker. Откройте выбранный порт в firewall сервера. Домен необязателен: если используете домен, его A/AAAA-запись должна указывать на сервер. HTTPS в этой версии не настраивается.

Для правильных ссылок OPML при работе за прокси задайте `public_base_url: "http://feeds.example.com:8080"`. При пустом значении используется HTTP и Host входящего запроса. При прямом доступе по IP это тоже работает.

Результаты проверки всех источников и известные ограничения: [docs/SOURCES.md](docs/SOURCES.md). Спецификация: [docs/SPEC.md](docs/SPEC.md).

## Хранение и обновления

SQLite `/data/feed.db` хранится в volume `feed-data`. Перезапуск и пересоздание контейнера сохраняют базу:

```sh
docker compose restart
docker compose up -d --build
```

**`docker compose down -v` удаляет базу.** Обычный `down` оставляет volume.

В RSS остаются последние 100 записей. Компактные идентификаторы уже обработанных URL хранятся без ограничения, чтобы удалённые из выдачи статьи не появлялись снова. При первом обходе остальные обнаруженные старые URL также помечаются обработанными: следующий опрос не подмешивает весь архив. Это не архив полного текста.

Безопасная резервная копия с краткой остановкой:

```sh
mkdir -p backups
docker compose stop feed
docker compose run --rm --no-deps --entrypoint tar feed -C /data -czf - . > backups/feed-data.tar.gz
docker compose start feed
```

Архив включает WAL/SHM, если они остались. Для восстановления остановите сервис и распакуйте архив в `/data` от пользователя контейнера. Сохраните отдельно `config.yaml`. Не копируйте только `feed.db` во время записи без SQLite backup API.

## Настройка

`config.yaml` читается при запуске. После изменений выполните `docker compose restart`.

```yaml
listen: ":8080"
database: /data/feed.db
public_base_url: ""
interval: 30m
timeout: 45s
initial_items: 20
max_items: 100
workers: 3
browser_path: /usr/bin/chromium
browser_no_sandbox: true
sources:
  - id: example
    name: Example Blog
    url: https://example.com/blog/
    feed_url: https://example.com/feed/ # Если найден подходящий RSS/Atom
    link_selector: "article h2 a"     # Ссылки на статьи в HTML
    url_pattern: '/blog/[^/?]+/?$'     # Фильтр абсолютного URL, Go regexp
    card_selector: "article"         # Карточка для заголовка, анонса, изображения
    content_selector: ".post-body"   # Необязательно: иначе Readability
    next_selector: "a.next"          # Обычная пагинация со ссылками
    max_pages: 3
    browser_fallback: true
```

`feed_url` используется первым. Если в нём меньше `initial_items`, HTML и дополнительные страницы дополняют начальную выборку. При сбое RSS используется HTML. Без явного RSS и без CSS-селектора выполняется поиск RSS/Atom в `<link rel="alternate">`; проверяйте, что это нужный раздел, а не общий поток или releases.

Дополнительные параметры источника:

- `listing_url`: адрес списка, отличающийся от публичного URL блога (например, HTML API IBM).
- `extra_listing_urls`: дополнительные страницы HTML или RSS; общий обход ограничен `max_pages`.
- `browser: true`: всегда выполнять JavaScript для списка; статьи сначала загружаются обычным HTTP.
- `wait_selector`: элемент, появления которого ждём в браузере.
- `load_more_selector`, `load_more_clicks`: кнопка загрузки или следующей страницы в браузере; сохраняются результаты до и после нажатий, дубли удаляются.
- `scroll_steps`: число прокруток вниз для списков с бесконечной подгрузкой.
- `adapter: cloudera`: специализированный разбор JSON Cloudera.

Chromium запускается отдельно для браузерных загрузок, максимум один одновременно. Контейнер работает от непривилегированного пользователя. `browser_no_sandbox: true` нужен в контейнере без доступных user namespaces; для запуска вне Docker можно отключить его. Сайты с CAPTCHA или блокировкой IP могут остаться недоступными даже в Chromium.

HTML статей очищается от скриптов и обработчиков событий; относительные ссылки преобразуются в абсолютные. Картинки загружаются читателем с оригинального сайта. Ошибки полного текста видны как предупреждения, а доступный анонс остаётся в RSS.

## Диагностика

Проверить ссылки без изменения базы; дополнительно извлечь одну статью:

```sh
docker compose run --rm feed -config /app/config.yaml -check -sample
docker compose run --rm feed -config /app/config.yaml -check -sample -source claude
```

Проверка пишет JSONL в stdout; ошибки обнаружения дают ненулевой exit code. Предупреждения о полном тексте не делают источник полностью недоступным. Для одного внепланового обновления остановите основной экземпляр, выполните `-once`, затем снова запустите его. Не запускайте несколько планировщиков на одной базе.

Локальная разработка (Go 1.26): скопируйте конфигурацию, измените `database` на доступный путь и `browser_path` на установленный Chromium.

```sh
go test -race ./...
go vet ./...
go build -o personal-feed ./cmd/personal-feed
./personal-feed -config config.local.yaml
```
