# CLAUDE.md — grpc-composition

## What this is

**grpc-composition** — тонкий Go-фреймворк для построения API Composition / BFF layer поверх unary gRPC сервисов.

Репо содержит и код фреймворка, и его продуктовое видение (`README.md`). Видение — источник истины для scope, принципов и roadmap. Когда меняется публичный API — синхронно правится `README.md` (включая секцию Implementation Status).

## Layout

- `README.md` — vision: scope, principles, Implementation Status, open questions
- `composition.go`, `errors.go` — core: `Proxy`, `Route`, `Binder`, `PathExtractor`, gRPC→HTTP error mapping
- `bind/` — биндеры (`Path`, `Query`, `JSON` + типизированные `*Int32`/`*Int64`/`*Bool`/`*As`)
- `composition_test.go` — все тесты ядра (внешний пакет `composition_test`)
- `examples/basic/` — runnable end-to-end demo (real `.proto` + bufconn gRPC server)

## Operating rules

- Язык документации (README, long-form комментарии) — русский. Идентификаторы, экспортируемое API, godoc — английский.
- Базовый роутер — `net/http` 1.22+ (`r.PathValue`). Chi/прочие — через `composition.SetDefaultPathExtractor` (один setter в `main`).
- Wire format для proto-сообщений — `protojson` (не `encoding/json`).
- `bind/` зависит только от stdlib и корневого `composition` пакета. **Никакого** chi или других роутеров в импортах.
- Не добавлять фичи вне текущего этапа roadmap'а, даже если «недолго».
- Setter в `bind.Path` / `bind.Query` возвращает `error`. Парс-ошибки автоматически бабблят как HTTP 400 c префиксом имени параметра.

## Toolchain notes

- `go.mod` указывает `go 1.25.0` (форсируется транзитивными deps `google.golang.org/grpc`). С `GOTOOLCHAIN=auto` (дефолт) Go скачивает 1.25 и собирает корректно. **Не понижать** до 1.22/1.23 — локальный `/usr/local/go` (1.23.4) сломан, см. auto-memory `env-go-install-broken`.
- `protoc-gen-go` (v1.34.2) уже установлен в `/usr/local/bin/protoc-gen-go` (из официального release tarball protobuf-go).
- `protoc-gen-go-grpc` (v1.5.1) уже установлен в `/usr/local/bin/protoc-gen-go-grpc` (собран из `/tmp/grpc-go-src/cmd/protoc-gen-go-grpc` с `GOTOOLCHAIN=go1.25.0`).
- Если плагинов нет (свежее окружение): см. auto-memory `env-go-install-broken` — `go install` напрямую не работает, нужно либо pre-built binary, либо `GOTOOLCHAIN=go1.25.0 go build`.

## Регенерация proto-кода

После любого изменения `.proto`:

```bash
cd examples/basic
protoc --go_out=. --go_opt=paths=source_relative \
       --go-grpc_out=. --go-grpc_opt=paths=source_relative \
       userpb/user.proto
```

Сгенерированные `*.pb.go` / `*_grpc.pb.go` коммитятся в репо.

## Test conventions

- Все тесты в `composition_test.go`, пакет `composition_test` (external test package).
- Mock gRPC client — обычный Go-struct с методами в сигнатуре `func(ctx, *Req, ...grpc.CallOption) (*Resp, error)`. **Не нужен** реальный `grpc.ClientConn` для unit-тестов.
- Для проверки `protojson` пути используется `google.golang.org/protobuf/types/known/wrapperspb` — `StringValue` это настоящий `proto.Message`, кодгена не нужно.
- `examples/basic` запускает in-process gRPC server через `google.golang.org/grpc/test/bufconn` — без сетевых портов на стороне gRPC, только HTTP-сервер на `:8080`.

## Commands

- `go build ./...`
- `go test ./...`
- `go run ./examples/basic` — поднимет HTTP на `:8080` поверх in-process gRPC
