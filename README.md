# grpc-composition

Lightweight Go framework для построения API Composition / BFF layer поверх unary gRPC сервисов.

Репозиторий содержит и код фреймворка, и продуктовое видение. Актуальный статус реализации — см. секцию [Implementation Status](#implementation-status).

---

## Position

Тонкий, opinionated Go-слой между HTTP и unary gRPC, который:

- режет boilerplate в proxy-endpoint'ах через **generics + typed callbacks** (не runtime reflection, не magic strings)
- остаётся неинвазивным: не управляет DI, config, connection lifecycle, auth
- разделяет concerns: compat ловится `buf`'ом, observability — стандартными OTel middleware'ами, framework отвечает только за HTTP↔gRPC

**Слоган:** *Composition owns routing and binding. Protobuf owns the wire. `buf` owns the compatibility. OTel middlewares own the tracing.*

---

## Problem

В microservice-архитектуре часто появляется слой:

```
Clients
   ↓
REST / BFF / API Composition
   ↓
Internal gRPC services
```

Существующие подходы:

**grpc-gateway**
- требует REST-аннотации в `.proto`
- HTTP concerns текут в internal contracts
- сложно независимо эволюционировать REST API
- aggregation/composition неудобны

**Ручной composition layer**
- огромное количество boilerplate
- repetitive handlers / binding / error handling
- сложно поддерживать consistency

**Цель grpc-composition** — дать тонкий слой, который убирает boilerplate, но не тащит с собой ни аннотаций в proto, ни тяжёлой framework-инфраструктуры.

---

## Базовые допущения

1. **gRPC contract compatibility — внешний concern.** Используется `buf breaking` / `protolock` / equivalent. Framework не проверяет совместимость proto.
2. **Wire format — `protojson`.** Корректно обрабатывает `oneof`, well-known types (`Timestamp`, `Duration`, `FieldMask`), presence semantics.
3. **Connection lifecycle — пользовательский.** `*grpc.ClientConn` создаётся снаружи и передаётся в generated clients. Framework их не оборачивает.
4. **Distributed tracing — через стандартные middlewares.** `otelhttp.NewHandler(...)` снаружи + `otelgrpc.UnaryClientInterceptor()` на grpc client. Framework только пробрасывает `r.Context()` в grpc invocation — propagation работает сам.

---

## Core Principles

1. **Code-first, не config-first.** Fluent Go API, не YAML DSL.
2. **Type safety over conciseness.** Magic strings → setter callbacks. Переименование поля в proto = compile error в роуте.
3. **No per-request reflection.** Биндинг резолвится через generics и closures на старте; runtime hot path — без descriptor walking.
4. **Proto-by-default.** REST shape совпадает с proto shape, пока вы явно не сказали иначе через `Map`/`Bind`. `Map` — для intentional API differentiation, не для safety.
5. **Incremental adoption.** Endpoint-by-endpoint миграция поверх существующего роутера.

---

## Core API

### Простой proxy

```go
r.Get("/users/{id}",
    app.Proxy(userClient.GetUser,
        bind.Path("id", func(req *pb.GetUserRequest, v string) error {
            req.Id = v
            return nil
        }),
    ),
)
```

Setter принимает raw-строку из path/query и возвращает `error` — это даёт явное место для парсинга в числовые/UUID/time поля и корректного surface'а ошибок как HTTP 400.

### Query params с типизированным парсингом

```go
r.Get("/users",
    app.Proxy(userClient.ListUsers,
        bind.QueryInt32("limit",  func(req *pb.ListUsersRequest, v int32) { req.Limit = v }),
        bind.QueryInt32("offset", func(req *pb.ListUsersRequest, v int32) { req.Offset = v }),
    ),
)
```

Для типов вне готового набора — generic `bind.PathAs` / `bind.QueryAs` с произвольным парсером:

```go
bind.PathAs("user_id", uuid.Parse, func(req *pb.Req, v uuid.UUID) {
    req.UserId = v.String()
})
```

Парс-ошибки автоматически бабблят как HTTP 400 с префиксом имени параметра, например: `{"error":"bind: limit: strconv.ParseInt: parsing \"oops\": invalid syntax"}`.

**Семантическое отличие:** `Path*` ожидает обязательный параметр (пустое значение → 400), `Query*` опционален (пустое → поле остаётся zero, ошибки нет).

Готовые хелперы: `PathInt32`, `PathInt64`, `PathBool`, `PathAs`, `QueryInt32`, `QueryInt64`, `QueryBool`, `QueryAs`. UUID/time/float — пока через `*As` с пользовательским парсером.

### JSON body + path

```go
r.Post("/orgs/{org_id}/members",
    app.Proxy(orgClient.AddMember,
        bind.JSON[pb.AddMemberRequest](),
        bind.Path("org_id", func(req *pb.AddMemberRequest, v string) error {
            req.OrgId = v
            return nil
        }),
    ),
)
```

Биндеры применяются в порядке указания — explicit composition. Если любой setter возвращает error, gRPC-вызов не происходит, клиент получает HTTP 400 с сообщением.

### Response mapping (intentional differentiation)

```go
app.Proxy(userClient.GetUser, bindGetUser).
    Map(func(resp *pb.GetUserResponse) any {
        return UserDTO{ID: resp.User.Id, DisplayName: resp.User.FullName}
    })
```

Без `Map` — `protojson` напрямую (proto-by-default). С `Map` — ваш shape под вашу ответственность.

### HTTP status code

```go
app.Proxy(userClient.CreateUser, bindCreate).
    OnSuccess(http.StatusCreated)
```

### Application setup

```go
app := composition.New(
    composition.WithErrorMapper(myMapper), // опционально
)

r := chi.NewRouter()
r.Use(otelhttp.NewMiddleware("api")) // tracing — стандартный middleware

userConn, _ := grpc.NewClient(addr,
    grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
)
userClient := pb.NewUserServiceClient(userConn)

r.Get("/users/{id}", app.Proxy(userClient.GetUser,
    bind.Path("id", func(req *pb.GetUserRequest, v string) error {
        req.Id = v
        return nil
    }),
))
```

---

## Error mapping

Default gRPC `Status` → HTTP:

```
NotFound           -> 404
InvalidArgument    -> 400
AlreadyExists      -> 409
PermissionDenied   -> 403
Unauthenticated    -> 401
FailedPrecondition -> 422
ResourceExhausted  -> 429
DeadlineExceeded   -> 504
Unavailable        -> 503
Aborted            -> 409
Canceled           -> 499
Internal/Unknown   -> 500
```

5xx по умолчанию **не** проксируют gRPC status message наружу — только request-id для корреляции с логами. Кастомизация — через `WithErrorMapper`.

Structured error details (`google.rpc.BadRequest`, `ErrorInfo`) → отложены в v0.2.

---

## Implementation Status

Легенда: ✅ Done · 📋 Next · ⏳ Planned · ❌ Out of scope

### v0.1 — Core proxy ✅ Complete

| Functional Requirement | Status |
|---|---|
| `Proxy[Req, Resp]` через generics, type-safe binding | ✅ Done |
| `bind.Path` (через `r.PathValue`, stdlib 1.22+) | ✅ Done |
| `bind.Query` | ✅ Done |
| `bind.JSON` (protojson + generic `proto.Message` constraint) | ✅ Done |
| Response mapping `Map(func(*Resp) any)` | ✅ Done |
| HTTP success status override `OnSuccess(int)` | ✅ Done |
| gRPC `Status` → HTTP code mapping + 5xx redaction | ✅ Done |
| `protojson` для `proto.Message` ответов | ✅ Done |
| chi-совместимость через `SetDefaultPathExtractor` | ✅ Done |
| Runnable example в `examples/basic/` (real `.proto` + bufconn) | ✅ Done |

### v0.2 — Production essentials

| Functional Requirement | Status |
|---|---|
| Typed sugar binders (`PathInt32`, `PathInt64`, `PathBool`, `PathAs`, `QueryInt32`, `QueryInt64`, `QueryBool`, `QueryAs`) | ✅ Done |
| `bind.Header` | ⏳ Planned |
| `Location` builder для POST → 201 | ⏳ Planned |
| Validation hook + `protovalidate` adapter | ⏳ Planned |
| Metadata forwarding (HTTP header → grpc metadata, allowlist) | ⏳ Planned |
| RFC 7807 error details (BadRequest field violations, ErrorInfo) | ⏳ Planned |
| `WithErrorMapper` для per-route override | ⏳ Planned |
| Дополнительные typed-sugar для `Float64`, `UUID`, `time.Time` | ⏳ Planned |

### v0.3 — Aggregation

| Functional Requirement | Status |
|---|---|
| `Aggregate(func)` для custom handlers | ⏳ Planned |
| `Parallel` + `Call` helpers с errgroup-семантикой | ⏳ Planned |
| `.Optional()` для partial-response | ⏳ Planned |

### v0.4+ — Sugar & tooling

| Functional Requirement | Status |
|---|---|
| OpenAPI генерация из binder metadata | ⏳ Planned |
| gin / echo адаптеры | ⏳ Planned |
| Optional codegen для setter'ов (если ergonomics окажется болью) | ⏳ Planned |
| Multipart / file upload (`bind.Multipart`) | ⏳ Planned |

### Out of scope (явно не делаем)

| Feature | Куда смотреть |
|---|---|
| ❌ Streaming / SSE / WebSockets | Connect-Go или ручной handler |
| ❌ Retries | grpc client interceptors |
| ❌ Circuit breakers | grpc interceptors / sony/gobreaker |
| ❌ Auth framework | HTTP middleware до `Proxy` |
| ❌ Caching | HTTP middleware |
| ❌ Field-level REST exposure (защита от leak новых proto-полей) | отдельный lint tool, не core |

---

## Architecture

```
HTTP Request
    ↓
Binders (typed closures, generic'нутые по Req)
    ↓
gRPC unary invocation (с r.Context() — даёт span propagation через otelgrpc)
    ↓
Error mapper (Status → HTTP code)  |  Response mapper (optional Map)
    ↓
protojson marshal
    ↓
HTTP Response
```

Все decisions резолвятся при построении роута; в runtime hot path только closures и стандартные библиотечные вызовы.

---

## Modules

```
/composition          // App, Proxy, Route
/composition/bind     // Path, Query, JSON
/composition/errors   // default mapper, code → status table
/composition/chi      // chi-specific adapter
```

Размер кода для v0.1 — порядка 400–600 строк.

---

## Success Criteria (v0.1)

1. **Simple proxy endpoint** — 4–6 строк, type-safe.
2. **Endpoint с response mapping** — 8–10 строк.
3. **Переименование proto-поля** → compile error в роуте, не runtime.
4. **Tracing**: `otelhttp.NewMiddleware(...)` снаружи + `otelgrpc` на client → working distributed tracing без framework-specific config.
5. **Покрытие**: CRUD на одном сервисе (GET by id, list with query, POST, PUT, DELETE).
6. **Документация**: не больше 20 минут от первого endpoint до running proxy.

---

## Open Questions

1. **Verbosity setter'ов** — если на практике это окажется реальной болью, рассмотреть optional codegen из proto descriptor (не runtime reflection).
2. ~~**Query / header type conversion sugar**~~ — решено в v0.2: shipped `PathInt32/64`, `PathBool`, `QueryInt32/64`, `QueryBool` плюс generic `PathAs`/`QueryAs`. UUID/time/float — отдельным мини-инкрементом.
3. **Partial-response semantics в `Aggregate`** — per-call `.Optional()` или policy-based (`MinSuccessful(N)`)?
4. **Multipart / file upload** — направление: `bind.Multipart` со stream в `bytes`-поле proto.
5. **Field-level REST exposure** (защита от утечки новых internal-полей в публичный REST) — отдельный lint/test tool, не core. Нужно ли вообще?
