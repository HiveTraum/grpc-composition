# CLAUDE.md — grpc-composition

## What this is

**grpc-composition** — тонкий Go-фреймворк для построения API Composition / BFF layer поверх unary gRPC сервисов.

Репо содержит и код фреймворка, и его продуктовое видение (`README.md`). Видение — источник истины для scope, принципов и roadmap. Когда меняется публичный API — синхронно правится `README.md`.

## Layout

- `README.md` — vision: scope, principles, roadmap, open questions
- `composition.go`, `errors.go` — core: `Proxy`, `Route`, `Binder`, gRPC→HTTP error mapping
- `bind/` — биндеры (`Path`, `Query`, далее `JSON`), зависят только от stdlib + `composition`
- `composition_test.go` — тесты ядра

## Operating rules

- Язык документации (README, long-form комментарии) — русский. Идентификаторы, экспортируемое API, godoc — на английском.
- Базовый роутер — `net/http` 1.22+ (`r.PathValue`). Поддержка chi/прочих — через configurable extractor, появится позже.
- Wire format для proto-сообщений — `protojson` (не `encoding/json`).
- `bind/` зависит только от stdlib и корневого `composition` пакета. Никакого chi или других роутеров в импортах.
- Не добавлять фичи вне текущего этапа roadmap'а, даже если "недолго".

## Commands

- `go build ./...`
- `go test ./...`
