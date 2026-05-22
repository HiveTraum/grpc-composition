# grpc-composition

Lightweight Go framework for building an API Composition / BFF layer on top of unary gRPC services.

This repository contains both the framework code and its product vision. For the current state of the implementation see the [Implementation Status](#implementation-status) section.

---

## Position

A thin, opinionated Go layer between HTTP and unary gRPC that:

- cuts boilerplate from proxy endpoints through **generics + typed callbacks** (no runtime reflection, no magic strings)
- stays non-invasive: does not own DI, config, connection lifecycle, or auth
- splits concerns: compatibility is caught by `buf`, observability comes from standard OTel middlewares, the framework only handles HTTP↔gRPC

**Tagline:** *Composition owns routing and binding. Protobuf owns the wire. `buf` owns the compatibility. OTel middlewares own the tracing.*

---

## Problem

A microservice architecture commonly ends up with this layer:

```
Clients
   ↓
REST / BFF / API Composition
   ↓
Internal gRPC services
```

Existing approaches:

**grpc-gateway**
- requires REST annotations in `.proto`
- HTTP concerns leak into internal contracts
- hard to evolve the REST API independently
- aggregation / composition is awkward

**Hand-rolled composition layer**
- enormous amounts of boilerplate
- repetitive handlers / binding / error handling
- hard to keep consistent

**The goal of grpc-composition** is to provide a thin layer that removes the boilerplate without dragging in proto annotations or heavyweight framework infrastructure.

---

## Base assumptions

1. **gRPC contract compatibility is an external concern.** Use `buf breaking` / `protolock` / equivalent. The framework does not police proto compatibility.
2. **The wire format is `protojson`.** It handles `oneof`, well-known types (`Timestamp`, `Duration`, `FieldMask`), and presence semantics correctly.
3. **Connection lifecycle is yours.** `*grpc.ClientConn` is created outside and passed into the generated clients. The framework does not wrap them.
4. **Distributed tracing comes from standard middlewares.** `otelhttp.NewHandler(...)` on the outside + `otelgrpc.UnaryClientInterceptor()` on the gRPC client. The framework only propagates `r.Context()` into the gRPC invocation — span propagation works on its own.

---

## Core Principles

1. **Code-first, not config-first.** Fluent Go API, not a YAML DSL.
2. **Type safety over conciseness.** Magic strings → setter callbacks. Renaming a proto field becomes a compile error in the route.
3. **No per-request reflection.** Binding resolves through generics and closures at startup; the runtime hot path has no descriptor walking.
4. **Proto-by-default.** The REST shape matches the proto shape unless you explicitly opt out via `Map` / custom binders. `Map` is for *intentional* API differentiation, not safety.
5. **Incremental adoption.** Endpoint-by-endpoint migration on top of an existing router.

---

## Core API

### Simple proxy

```go
r.Get("/users/{id}",
    app.Proxy(userClient.GetUser,
        bind.PathString("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
    ),
)
```

For string fields, `PathString` / `QueryString` are infallible: assigning a string can't fail, so the setter doesn't need to return `error`. Use the generic `bind.Path` / `bind.Query` (with `func(*Req, string) error`) when you need to validate the value or parse it into a non-string field — parse errors automatically surface as HTTP 400.

### Query params with typed parsing

```go
r.Get("/users",
    app.Proxy(userClient.ListUsers,
        bind.QueryInt32("limit",  func(req *pb.ListUsersRequest, v int32) { req.Limit = v }),
        bind.QueryInt32("offset", func(req *pb.ListUsersRequest, v int32) { req.Offset = v }),
    ),
)
```

For types outside the typed-sugar set, use generic `bind.PathAs` / `bind.QueryAs` with your own parser:

```go
bind.PathAs("user_id", uuid.Parse, func(req *pb.Req, v uuid.UUID) {
    req.UserId = v.String()
})
```

Parse errors automatically bubble up as HTTP 400 with the parameter name as a prefix, e.g. `{"error":"bind: limit: strconv.ParseInt: parsing \"oops\": invalid syntax"}`.

**Semantic difference:** `Path*` treats the parameter as required (an empty value yields a 400); `Query*` is optional (an empty value leaves the field at zero, no error).

Available helpers:

- **Path**: `PathString`, `PathInt32`, `PathInt64`, `PathFloat64`, `PathBool`, `PathEnum`, `PathAs`
- **Query**: `QueryString`, `QueryInt32`, `QueryInt64`, `QueryFloat64`, `QueryBool`, `QueryEnum`, `QueryAs`
- **Header**: `HeaderString`, `HeaderInt32`, `HeaderInt64`, `HeaderFloat64`, `HeaderBool`, `HeaderAs` (plus generic `Header` with error)

UUID / time — through `*As` with a user-supplied parser. `HeaderEnum` is planned for v0.3.

### Protobuf enums

```go
r.Get("/users",
    app.Proxy(userClient.ListUsers,
        bind.QueryEnum("role", func(req *pb.ListUsersRequest, v pb.Role) { req.Role = v }),
    ),
)
```

`PathEnum` / `QueryEnum` accept the canonical proto name (`ROLE_ADMIN`) or its numeric value (`2`). Matching is **strict and case-sensitive** to keep typo detection sharp and behavior aligned with `protojson`; `?role=admin` returns 400. For looser semantics, build a custom parser with `PathAs` / `QueryAs`.

For enum fields in a JSON request body, no extra helper is needed — `protojson.Unmarshal` (used by `bind.BodyJSON`) already accepts both names and numbers.

### Request body

The `Body*` family covers everything from "REST body matches the proto shape" (most common) to "REST body has a different shape than the proto" (intentional DTO layer) to "non-JSON formats" (escape hatch).

```go
// REST body matches the proto shape — direct protojson:
bind.BodyJSON[pb.AddMemberRequest]()

// REST shape differs and mapping can fail (parsing, validation):
bind.BodyJSONInto(func(dto CreateUserDTO, req *pb.CreateUserRequest) error {
    parts := strings.SplitN(dto.FullName, " ", 2)
    if len(parts) < 2 {
        return fmt.Errorf("full_name must contain first and last name")
    }
    req.GivenName, req.FamilyName, req.Email = parts[0], parts[1], dto.Email
    return nil
})

// REST shape differs but mapping is a pure field-copy (no failure mode):
bind.BodyJSONMap(func(dto CreateUserDTO, req *pb.CreateUserRequest) {
    req.Name = dto.FullName
    req.Email = dto.Email
})

// Non-JSON formats (YAML, raw protobuf wire, form-encoded, ...):
bind.Body(func(data []byte, req *pb.Req) error {
    return yaml.Unmarshal(data, req)
})
```

| Helper | When |
|---|---|
| `bind.BodyJSON[Req]()` | REST body shape matches proto shape; uses `protojson` (handles `oneof`, well-known types, presence correctly) |
| `bind.BodyJSONInto[Req, DTO](apply ... error)` | REST shape differs; mapping can fail |
| `bind.BodyJSONMap[Req, DTO](apply)` | REST shape differs; mapping is infallible field-copy |
| `bind.Body[Req](parse)` | Not JSON; user-supplied parser handles everything |

Combine with other binders in one route — binders run in order:

```go
r.Post("/orgs/{org_id}/members",
    app.Proxy(orgClient.AddMember,
        bind.BodyJSON[pb.AddMemberRequest](),
        bind.PathString("org_id", func(req *pb.AddMemberRequest, v string) { req.OrgId = v }),
    ),
)
```

If any binder returns an error, the gRPC call does not happen and the client receives HTTP 400 with the message.

### Response mapping (intentional differentiation)

```go
app.Proxy(userClient.GetUser, bindGetUser).
    Map(func(resp *pb.GetUserResponse) any {
        return UserDTO{ID: resp.User.Id, DisplayName: resp.User.FullName}
    })
```

Without `Map` — straight `protojson` (proto-by-default). With `Map` — your shape, your responsibility.

### HTTP status code

```go
app.Proxy(userClient.CreateUser, bindCreate).
    OnSuccess(http.StatusCreated)
```

### Application setup

```go
// App carries cross-cutting concerns (currently HTTP→gRPC metadata
// forwarding). Wrap the router with app.Handler to apply them globally.
app := composition.New(
    composition.WithMetadataForward("authorization", "x-request-id", "accept-language"),
)

// Optional: customize the package-level error mapper (defaults to RFC 7807).
// Per-route override is also available: route.WithErrorMapper(fn).
composition.SetDefaultErrorMapper(myMapper)

r := http.NewServeMux()

userConn, _ := grpc.NewClient(addr,
    grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
)
userClient := pb.NewUserServiceClient(userConn)

r.Handle("GET /users/{id}", composition.Proxy(userClient.GetUser,
    bind.PathString("id", func(req *pb.GetUserRequest, v string) { req.Id = v }),
))

// Wrap with app.Handler so the configured headers flow into outgoing
// gRPC metadata; combine with otelhttp on the outside for tracing.
handler := otelhttp.NewMiddleware("api")(app.Handler(r))
http.ListenAndServe(":8080", handler)
```

### HTTP → gRPC metadata forwarding

`composition.WithMetadataForward(headers...)` declares an **explicit allowlist** of HTTP request headers to forward into outgoing gRPC metadata. There is no wildcard — listing what flows downstream is the safe default.

```go
app := composition.New(
    composition.WithMetadataForward("Authorization", "X-Request-ID"),
)
handler := app.Handler(mux)
```

Header names are matched case-insensitively. Multi-value headers preserve all values. The forwarded headers reach any gRPC method called via `Proxy` because the framework hands `r.Context()` to the gRPC client, and `app.Handler` injected outgoing metadata into that context.

Use this for auth tokens, request-id correlation, locale (`Accept-Language`), tenant ids, etc.

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

### RFC 7807 response shape

The default error body follows [RFC 7807](https://datatracker.ietf.org/doc/html/rfc7807) with two gRPC-aware extensions, and the response is served as `application/problem+json`:

```json
{
  "type": "billing.example.com/RATE_LIMIT_EXCEEDED",
  "title": "Too Many Requests",
  "status": 429,
  "detail": "too many requests",
  "reason": "RATE_LIMIT_EXCEEDED",
  "metadata": { "retry_after": "60" },
  "errors": [
    { "field": "user.email", "description": "must be a valid email" }
  ]
}
```

- `errors[]` is populated from `google.rpc.BadRequest.FieldViolations` attached to the gRPC status via `status.WithDetails`. This is what `protovalidate-go` produces — field-level validation failures appear here.
- `reason`, `type`, `metadata` are populated from `google.rpc.ErrorInfo`.

### 5xx redaction

5xx responses do **not** propagate the upstream gRPC status message or details — they return a generic `"detail": "internal error"` so server-side state never leaks to clients. Pair with a request-id middleware for log correlation.

### Customization

- Per route: `route.WithErrorMapper(fn)` overrides for one endpoint
- Globally: `composition.SetDefaultErrorMapper(fn)` replaces the default mapper at program start

A custom mapper can return any body shape; only `composition.ProblemDetails` triggers the `application/problem+json` Content-Type. Other shapes get `application/json`.

---

## Implementation Status

Legend: ✅ Done · 📋 Next · ⏳ Planned · ❌ Out of scope

### v0.1 — Core proxy ✅ Complete

| Functional Requirement | Status |
|---|---|
| `Proxy[Req, Resp]` via generics, type-safe binding | ✅ Done |
| `bind.Path` (via `r.PathValue`, stdlib 1.22+) | ✅ Done |
| `bind.Query` | ✅ Done |
| `bind.BodyJSON` (protojson + generic `proto.Message` constraint) — renamed from `bind.JSON` in v0.2 | ✅ Done |
| Response mapping `Map(func(*Resp) any)` | ✅ Done |
| HTTP success status override `OnSuccess(int)` | ✅ Done |
| gRPC `Status` → HTTP code mapping + 5xx redaction | ✅ Done |
| `protojson` serialization for `proto.Message` responses | ✅ Done |
| chi compatibility via `SetDefaultPathExtractor` | ✅ Done |
| Runnable example in `examples/basic/` (real `.proto` + bufconn) | ✅ Done |

### v0.2 — Production essentials

| Functional Requirement | Status |
|---|---|
| Typed sugar binders (`PathString`, `PathInt32`, `PathInt64`, `PathBool`, `PathEnum`, `PathAs`, `QueryString`, `QueryInt32`, `QueryInt64`, `QueryBool`, `QueryEnum`, `QueryAs`) | ✅ Done |
| Body family rename + DTO variants (`bind.BodyJSON`, `bind.BodyJSONInto`, `bind.BodyJSONMap`, `bind.Body`); renames `bind.JSON` → `bind.BodyJSON` (breaking) | ✅ Done |
| `bind.Header` family (`Header`, `HeaderString`, `HeaderInt32`, `HeaderInt64`, `HeaderFloat64`, `HeaderBool`, `HeaderAs`) | ✅ Done |
| Metadata forwarding (HTTP header → gRPC metadata, allowlist) via `App.Handler` | ✅ Done |
| RFC 7807 error details (BadRequest field violations, ErrorInfo) + `application/problem+json` | ✅ Done |
| `WithErrorMapper` per-route + `SetDefaultErrorMapper` package-level | ✅ Done |
| Additional typed sugar for `Float64` (Path/Query/Header) | ✅ Done |

> **Validation:** use [`protovalidate-go`](https://github.com/bufbuild/protovalidate-go) as a gRPC unary client interceptor. Violations arrive as `codes.InvalidArgument` with `status.WithDetails(*errdetails.BadRequest)` and surface as HTTP 400 field errors via RFC 7807 error details (see above). No framework-level hook is needed.

### v0.3 — Aggregation

| Functional Requirement | Status |
|---|---|
| `Aggregate(func)` for custom handlers | ⏳ Planned |
| `Parallel` + `Call` helpers with errgroup semantics | ⏳ Planned |
| `.Optional()` for partial responses | ⏳ Planned |

### v0.4+ — Sugar & tooling

| Functional Requirement | Status |
|---|---|
| OpenAPI generation from binder metadata | ⏳ Planned |
| gin / echo adapters | ⏳ Planned |
| Optional codegen for setters (if ergonomics turn out to be a real pain point) | ⏳ Planned |
| Multipart / file upload (`bind.Multipart`) | ⏳ Planned |
| `Binder` metadata refactor (struct with `PathParam` / `QueryParam` fields) + `Mount` helper that validates declared path params against the route pattern at startup, catching binder/pattern mismatches at boot rather than first request | ⏳ Planned |
| `HeaderEnum` typed binder (deferred from v0.2 along with other header infrastructure) | ⏳ Planned |
| Typed sugar for `UUID`, `time.Time` (need external dep + format-choice decisions) | ⏳ Planned |

### Out of scope (explicit non-goals)

| Feature | Where to look instead |
|---|---|
| ❌ Streaming / SSE / WebSockets | Connect-Go or a hand-rolled handler |
| ❌ Retries | gRPC client interceptors |
| ❌ Circuit breakers | gRPC interceptors / sony/gobreaker |
| ❌ Auth framework | HTTP middleware in front of `Proxy` |
| ❌ Caching | HTTP middleware |
| ❌ Field-level REST exposure (guard against leaking new proto fields) | Separate lint tool, not core |

---

## Architecture

```
HTTP Request
    ↓
Binders (typed closures, generic over Req)
    ↓
gRPC unary invocation (with r.Context() — span propagation via otelgrpc)
    ↓
Error mapper (Status → HTTP code)  |  Response mapper (optional Map)
    ↓
protojson marshal
    ↓
HTTP Response
```

All decisions resolve at route construction time; the runtime hot path runs only closures and standard-library calls.

---

## Modules

```
/composition          // App, Proxy, Route
/composition/bind     // Path, Query, JSON
/composition/errors   // default mapper, code → status table
/composition/chi      // chi-specific adapter
```

Code size for v0.1 — on the order of 400–600 lines.

---

## Success Criteria (v0.1)

1. **Simple proxy endpoint** — 4–6 lines, type-safe.
2. **Endpoint with response mapping** — 8–10 lines.
3. **Renaming a proto field** → compile error in the route, not at runtime.
4. **Tracing**: `otelhttp.NewMiddleware(...)` on the outside + `otelgrpc` on the client → working distributed tracing with no framework-specific config.
5. **Coverage**: CRUD on a single service (GET by id, list with query, POST, PUT, DELETE).
6. **Documentation**: no more than 20 minutes from first endpoint to a running proxy.

---

## Open Questions

1. **Setter verbosity** — if this turns out to be a real pain point in practice, consider optional codegen from the proto descriptor (not runtime reflection).
2. ~~**Query / header type conversion sugar**~~ — resolved in v0.2: shipped `PathInt32/64`, `PathBool`, `QueryInt32/64`, `QueryBool` plus generic `PathAs`/`QueryAs`. UUID / time / float — separate mini-increment.
3. **Partial-response semantics in `Aggregate`** — per-call `.Optional()` or policy-based (`MinSuccessful(N)`)?
4. **Multipart / file upload** — direction: `bind.Multipart` streaming into a `bytes` field of the proto.
5. **Field-level REST exposure** (protection against accidentally leaking newly-added internal proto fields into a public REST API) — separate lint/test tool, not core. Is it needed at all?
6. **Response-driven response headers** — `Location` for `POST → 201`, `ETag` for conditional requests, `X-Total-Count` for pagination, etc. A generic helper that derives outgoing HTTP headers from the gRPC response (`func(*Resp) http.Header`) would cover all of these in one shot. Worth a dedicated builder, or leave it to `Map` + a custom outer handler?

---

## License

Apache License 2.0 — see [LICENSE](LICENSE).
