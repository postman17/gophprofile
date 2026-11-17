# Gophprofile

Сервис аватаров: HTTP API → PostgreSQL / S3 (MinIO) → Kafka → worker, который создаёт миниатюры.

## Запуск

```sh
cp .env.example .env
docker compose up -d --build
```

Сервисы мониторинга запускаются вместе с приложением. Для локального окружения используются пароли из `.env.example`.

- Приложение: http://localhost:3200/web/upload
- Grafana: http://localhost:3000 — `admin` / `admin` (переопределяются в `.env`). Папка **Gophprofile** содержит Service overview, HTTP RED, Resources и Business KPIs.
- Jaeger: http://localhost:16686 — сервисы `gophprofile-server` и `gophprofile-worker`.
- Prometheus: http://localhost:9090 — targets, запросы и правила алертов.
- Alertmanager: http://localhost:9093 — активные алерты и silences.
- Loki: http://localhost:3100/ready — поиск логов доступен через Grafana Explore → Loki.

Порты мониторинга привязаны к localhost. Это локальный стенд: для внешнего развёртывания нужны аутентификация, TLS, секреты и sizing. Alloy читает Docker socket для обнаружения контейнеров и чтения stdout; используйте его только на доверенном Docker-хосте. Фильтр ограничивает сбор логов сервисами server/worker текущего Compose-проекта.

## Трейсы и логи

HTTP middleware извлекает W3C `traceparent` / `tracestate` и baggage, создаёт серверный span, учитывает итоговый HTTP status и возвращает `X-Trace-ID`. Метрики используют шаблоны маршрутов, а неизвестные URL объединяются в `unmatched`.

Загрузка создаёт бизнес-span `avatar.upload` с user ID, именем, размером и MIME. Запросы pgx создают клиентские PostgreSQL spans без SQL-параметров и текста запросов. S3-операции создают клиентские spans, HTTP transport AWS SDK добавляет дочерние HTTP spans и propagation. Миграции goose выполняются до обслуживания запросов отдельно от pgx tracing.

Kafka producer записывает контекст в заголовки сообщения; consumer восстанавливает его до обработки, включая retries и DLQ. В Jaeger видна цепочка HTTP → upload → S3 / DB → Kafka publish → worker → S3 / DB. Spanmetrics в OpenTelemetry Collector экспортирует агрегаты для Jaeger Monitor (SPM). Servicegraph connector строит зависимости по spans; карта и интенсивность связей доступны на Service overview в Grafana. Связи между сервисами видны в распределённом трейсе; глобальный Jaeger Dependencies DAG требует отдельного dependency-processing job и постоянного trace storage.

Логи JSON пишутся в stdout через `slog`. Контекстные записи получают `trace_id` и `span_id`; уровень задаётся `LOG_LEVEL=DEBUG|INFO|WARN|ERROR`. Alloy передаёт их в Loki. В Grafana у логов есть ссылка TraceID → Jaeger, а у трейсов — переход к логам.

```logql
{service=~"server|worker"} | json | level="ERROR"
{service=~"server|worker"} | json | trace_id="<trace-id>"
```

User ID и trace ID остаются полями JSON и атрибутами spans, не labels Prometheus/Loki. Это ограничивает количество временных рядов. Файловые имена и user ID могут содержать персональные данные: учитывайте это при настройке доступа и retention.

## Метрики

Оба процесса обслуживают `/metrics` на отдельном внутреннем `METRICS_ADDR=:9090` (не опубликован на хост). Экспортируются:

- `http_requests_total{method,route,status}`, `http_request_duration_seconds{method,route}`: итоговый ответ, включая ошибки маршрутизации/валидации; health имеет отдельный route.
- `avatars_uploads_total{status}`, `avatars_upload_duration_seconds{status}`: все попытки, дошедшие до сервисного метода Upload, включая ошибки валидации. HTTP-отказы до Upload учитываются только в HTTP-метриках.
- `avatars_storage_bytes`, `avatars_storage_objects`: фактический размер и число объектов всего S3 bucket, включая миниатюры. API пересчитывает их раз в минуту с пагинацией; после перезапуска значения восстанавливаются из S3. При неудаче сохраняется предыдущий снимок, свежесть отражает `avatars_storage_last_success_timestamp_seconds`. Инвентаризация требует `s3:ListBucket`; для большого bucket нужен отдельный inventory pipeline. При нескольких API-репликах используйте `max`, а не `sum` этих gauges.
- `avatar_events_total{operation,topic,status}`, `avatar_event_duration_seconds{operation,topic}`: публикация, обработка (включая retries) и отправка в DLQ.
- `db_pool_*`: total/acquired/idle/max connections отдельно для каждого процесса; стандартные `go_*` и `process_*`.
- `kafka_consumergroup_lag`: глубина необработанной очереди по committed offsets, включая сообщения в обработке, из kafka-exporter. Не количество buffered сообщений клиента. До создания consumer group метрика может отсутствовать.

Prometheus хранит данные 7 дней, Loki — 7 дней; данные Grafana, Alloy и Alertmanager сохраняются в volumes. Jaeger all-in-one использует память: трейсы теряются при рестарте. Для постоянного хранения замените его конфигурацией с поддерживаемым persistent backend.

## Алерты

Prometheus отправляет в Alertmanager правила: HTTP/upload errors >10% за 5 минут, p95 HTTP/upload >5 секунд в течение 2 минут, недоступность процесса, Kafka lag >100 и устаревшая S3-инвентаризация. Доли ошибок считаются через `sum(rate(...))`, p95 — через `histogram_quantile` с агрегацией buckets по `le`.

Активные алерты доступны в UI. В `deploy/observability/alertmanager.yml` настроен локальный receiver без внешней доставки. Для отправки уведомлений добавьте свой email/webhook/Slack receiver и секреты; реквизиты не включены в репозиторий.

## Запуск вне Docker

Приложению нужны `DATABASE_URL`, `KAFKA_BROKERS`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`; остальные настройки — в `internal/config/config.go`. Для одновременного запуска server и worker на одном хосте задайте разные `METRICS_ADDR` (например `:9091` и `:9092`).

`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` включает экспорт OTLP/HTTP (порт collector по умолчанию доступен только внутри Compose; опубликуйте его для локального Go-процесса). Поддерживаются стандартные OTEL exporter headers/TLS/timeout, `OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_SAMPLER` и `OTEL_TRACES_SAMPLER_ARG`. Без endpoint spans создаются локально без сетевого экспорта. В Compose sampling 100%; для нагрузки можно выбрать `parentbased_traceidratio` и аргумент `0.1`. Traces корректно выгружаются при штатном завершении.

## Проверка

```sh
go test -race ./...
go vet ./...
docker compose config --quiet
docker compose exec prometheus promtool check config /etc/prometheus/prometheus.yml
docker compose exec prometheus promtool check rules /etc/prometheus/alerts.yml
curl -fsS http://localhost:3200/health
python3 scripts/smoke_observability.py
```

Загрузите изображение в `/web/upload`, откройте галерею и удалите аватар: проверьте HTTP и worker в одном трейсе Jaeger, связанные логи в Grafana и изменение бизнес-метрик (инвентаризация S3 обновляется в течение минуты).
