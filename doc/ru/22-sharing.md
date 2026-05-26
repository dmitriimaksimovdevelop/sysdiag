# Глава 22: Шеринг отчётов

## Обзор

`melisai share` превращает JSON-отчёт в одну ссылку, которую можно бросить в Slack, инцидент-канал или Jira-тикет. Получатель открывает её в любом современном браузере и видит диагностику отрендеренной — без установки melisai, без JSON-вьюера, без агентов.

URL'а бывает две формы. CLI по умолчанию делает короткую и автоматически откатывается на длинную, когда бэкенд недоступен — одна и та же команда работает на хосте с интернетом и на airgapped-сервере.

[![melisai share demo — нагруженный сервер, health 2/100](images/share-demo-hero.png)](https://melisai.dev/r/uJOzZUjb)

Живое демо: <https://melisai.dev/r/uJOzZUjb>

## Две формы URL

| Режим | Вид URL | Когда срабатывает | Хранение |
|-------|---------|--------------------|----------|
| **Short link** | `https://melisai.dev/r/Xa9bC3kp` | Дефолт. CLI делает POST на `melisai.dev/api/r` | Бэкенд хранит gzip-блоб в SQLite |
| **Fragment fallback** | `https://melisai.dev/r#H4sIAAAA…` | Upload упал (сеть/таймаут/5xx), или `--offline` | Никакого — URL и есть payload |

Обе ссылки декодируются в одну и ту же страницу: meta-блок (hostname/kernel/profile/duration), health score 0–100, USE-метрики барами, список аномалий, рекомендации с copy-paste командами.

## Что внутри payload

`melisai share` не загружает весь отчёт. Берётся только `Summary` плюс минимальный заголовок:

```json
{
  "v": 1,
  "meta": {
    "hostname":     "prod-rtb-01",
    "kernel":       "6.8.0-90-generic",
    "cpus":         32,
    "memory_gb":    128,
    "profile":      "standard",
    "duration":    "30s",
    "timestamp":    "2026-05-25T10:00:00Z",
    "tool_version": "0.6.0"
  },
  "summary": {
    "health_score":   68,
    "anomalies":     [ /* 0..N */ ],
    "resources":     { /* USE-метрика на ресурс */ },
    "recommendations":[ /* 0..N */ ]
  }
}
```

Viewer на `melisai.dev/r/` умеет рендерить только `v: 1`. Любое изменение схемы обязано остаться совместимым со старыми клиентами — иначе вместо краша показывается понятное «unsupported payload version».

Wire-формат: `base64url(gzip(json))`. Типичные размеры encoded URL:

| Профиль | JSON | gzip+base64url |
|---------|------|----------------|
| `quick`, здоровый сервер | ~10 KB | ~1.5 KB |
| `standard`, нагруженный сервер с 15 anomalies + 10 recs | ~25 KB | ~3–5 KB |
| `deep` со стеками/гистограммами | ~80–150 KB | summary всё равно ~5–10 KB (стеки и гистограммы в share не уходят) |

## Privacy

Payload несёт идентифицирующие данные: hostname, версию ядра, evidence-строки аномалий («`TCPRcvQDrop=1.2k/s observed across 3 interfaces`»), evidence рекомендаций. Относитесь к URL как к самому JSON-отчёту.

Если hostname чувствительный — затрите его перед шарингом: `jq '.metadata.hostname = "redacted"' report.json | melisai share -`.

## Флаги CLI

```
melisai share [flags] <report.json>

  --api-url     string   бэкенд для коротких ссылок (default https://melisai.dev/api/r)
  --base-url    string   префикс viewer'а для fragment fallback (default https://melisai.dev/r)
  --offline             пропустить backend, сразу fragment URL
  --timeout     duration бюджет на upload до отката на fragment (default 10s)
```

Чтобы прочитать отчёт из stdin, используйте `-` вместо пути: `melisai collect ... -o - | melisai share -`.

## Self-hosting бэкенда

Backend — это бинарь `melisai-share-api` из этого же репо:

```bash
go build ./cmd/melisai-share-api
./melisai-share-api \
  --addr=:8080 \
  --db=/var/lib/melisai-share-api/store.db \
  --public-base=https://share.example.com/r \
  --max-body-bytes=1048576 \
  --retention=2160h \
  --cleanup-interval=1h
```

Viewer — статичный HTML+JS — скопируйте `apps/melisai-site/public/r/index.html` (репо [`hetzner-k8s-infra`](https://github.com/dmitriimaksimovdevelop/hetzner-k8s-infra)) за любой nginx, который проксирует `/api/r` на бинарь. Auth на POST нет — защищайте rate-limit'ом и firewall'ом.

## HTTP-контракт

Backend отдаёт три эндпойнта. Всё остальное — 404.

| Метод | Путь | Body | Ответ |
|--------|------|------|----------|
| `POST` | `/api/r` | gzip-байты (должны начинаться с `0x1f 0x8b`), ≤ 1 MB | `201` `{"code":"Xa9bC3kp","url":"https://…/r/Xa9bC3kp"}` |
| `GET`  | `/api/r/{code}` | — | `200` сырые gzip-байты (`application/octet-stream`, `Cache-Control: public, max-age=300`) |
| `GET`  | `/healthz` | — | `200` `{"status":"ok","count":N}` |

Viewer тащит `/api/r/{code}` с `cache: 'no-store'`, чтобы retention sweep и takedown'ы не маскировались браузерным кэшем.

## Security

- **Пространство кодов.** 8 base62-символов = 62⁸ ≈ 2.18 × 10¹⁴ слотов, генерируются `crypto/rand` с rejection sampling. Угадать нельзя, энумерации нет.
- **Body cap.** Бэкенд режет > 1 MB и всё, что не начинается с gzip-magic (`0x1f 0x8b`).
- **Rate limit.** Reference-конфиг nginx режет writes 20/min на реальный client IP. Подкручивайте по вкусу.
- **Schema validation.** Viewer отвергает payload'ы, чей `v` не равен `SUPPORTED_VERSION`.
- **XSS.** Все поля payload'а рендерятся через `textContent`. Viewer никогда не отдаёт недоверенные данные в `innerHTML`, `eval`, `Function` или `setTimeout(string)`.

## Файлы исходников

| Файл | Строк | Назначение |
|------|-------|------------|
| `internal/share/share.go` | ~140 | Схема payload, `Encode`/`Decode`, `BuildURL` |
| `internal/share/client.go` | ~75 | HTTP-клиент CLI для upload коротких ссылок |
| `cmd/melisai/share.go` | ~100 | Cobra subcommand, проводка fragment fallback |
| `internal/shareapi/codec.go` | ~70 | Генератор 8-символьных base62 кодов, `ValidCode` |
| `internal/shareapi/store.go` | ~120 | SQLite-стор (open/put/get/count/delete-older-than) |
| `internal/shareapi/handlers.go` | ~190 | POST/GET/healthz handlers, body cap, collision retry |
| `cmd/melisai-share-api/main.go` | ~165 | Entry point бинаря, retention-цикл, graceful shutdown |
