# grpc-composition

Lightweight Go framework для построения API Composition / BFF layer поверх unary gRPC сервисов.

Этот репозиторий — **только документация**: продуктовое видение, спецификация, roadmap. Код фреймворка живёт отдельно.

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
        bind.Path("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
    ),
)
```

### Query params

```go
r.Get("/users",
    app.Proxy(userClient.ListUsers,
        bind.Query("limit",  func(req *pb.ListUsersRequest, v string) { /* parse, set */ }),
        bind.Query("offset", func(req *pb.ListUsersRequest, v string) { /* parse, set */ }),
    ),
)
```

### JSON body + path

```go
r.Post("/orgs/{org_id}/members",
    app.Proxy(orgClient.AddMember,
        bind.JSON[pb.AddMemberRequest](),
        bind.Path("org_id", func(req *pb.AddMemberRequest, v string) { req.OrgId = v }),
    ),
)
```

Биндеры применяются в порядке указания — explicit composition.

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
    bind.Path("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
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

## v0.1 Scope

### Включено

- unary gRPC proxying через generics
- typed binding: `bind.Path`, `bind.Query`, `bind.JSON`
- response mapping (`Map`)
- HTTP success status override (`OnSuccess`)
- gRPC `Status` → HTTP code mapping (базовый)
- chi adapter
- net/http совместимость

### Отложено

| Feature | Target |
|---|---|
| Aggregation helpers (`Parallel`, `Call`, `.Optional()`) | v0.3 |
| `bind.Header` | v0.2 |
| `Location` builder | v0.2 |
| Validation hook + protovalidate adapter | v0.2 |
| Metadata forwarding (HTTP header → grpc metadata) | v0.2 — отдельно от tracing |
| RFC 7807 error details (BadRequest field violations, ErrorInfo) | v0.2 |
| Streaming / SSE / WS | вне scope; Connect-Go |
| Retries / circuit breakers | вне scope; grpc interceptors |
| Auth framework | вне scope; HTTP middleware до `Proxy` |
| Caching | вне scope; HTTP middleware |
| OpenAPI генерация | отдельный tool в будущем |
| gin / echo first-class | сообщество |

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

## Roadmap

| Версия | Скоуп |
|---|---|
| **v0.1** | Core API: `Proxy` + `bind` + `Map` + `OnSuccess` + базовый error mapping + chi |
| **v0.2** | Production essentials: metadata forwarding, error details (RFC 7807), `bind.Header`, `Location`, validation hook + `protovalidate` adapter |
| **v0.3** | Aggregation: `Parallel`, `Call`, `.Optional()`, partial response semantics |
| **v0.4+** | OpenAPI tooling, gin / echo адаптеры, optional codegen для setter'ов (если ergonomics окажется болью) |

---

## Open Questions

1. **Verbosity setter'ов** — если на практике это окажется реальной болью, рассмотреть optional codegen из proto descriptor (не runtime reflection).
2. **Query / header type conversion sugar** — `bind.QueryInt`, `bind.QueryTime`, или ручные setter'ы по месту? Решается по частоте use case.
3. **Partial-response semantics в `Aggregate`** — per-call `.Optional()` или policy-based (`MinSuccessful(N)`)?
4. **Multipart / file upload** — направление: `bind.Multipart` со stream в `bytes`-поле proto.
5. **Field-level REST exposure** (защита от утечки новых internal-полей в публичный REST) — отдельный lint/test tool, не core. Нужно ли вообще?
