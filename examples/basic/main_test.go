// Tests for the example service. These exist not just to verify the
// example works, but to demonstrate the patterns we recommend for
// testing real grpc-composition services:
//
//  1. Spin up the full stack via the same wiring as production (here:
//     newApp), then drive it with [httptest.NewServer]. The HTTP server,
//     the in-process gRPC server, and the binders all run end-to-end —
//     bugs in any layer surface as assertion failures.
//
//  2. Assert on response *content*, not just status codes. This is what
//     catches subtle binder mistakes (wrong path-param name, missed
//     query field) that would otherwise pass a 200-vs-not-200 check.
//
//  3. Use protojson to decode proto-typed responses — encoding/json
//     does not handle enums, well-known types, oneof or presence
//     correctly. The decodeProto helper at the top of the file is the
//     recommended pattern.
//
// See TestGetUser_PinsPathParamBinding and TestDemonstrateBindMismatch
// at the bottom for the specific "hello → start_city_code" scenario
// from the README discussion.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/HiveTraum/grpc-composition"
	"github.com/HiveTraum/grpc-composition/bind"
	"github.com/HiveTraum/grpc-composition/examples/basic/userpb"
)

// newTestServer is the recommended setup for an end-to-end composition
// test: real app handler + real httptest server, the way a black-box
// integration test would run against the actual binary.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler, cleanup := newApp()
	srv := httptest.NewServer(handler)
	t.Cleanup(func() {
		srv.Close()
		cleanup()
	})
	return srv
}

// decodeProto decodes a protojson HTTP body into a proto message.
// Reach for this instead of [encoding/json] when the response is a
// generated protobuf type — it handles enums (as proto names), well-
// known types, oneof, and presence semantics correctly.
func decodeProto(t *testing.T, body io.Reader, msg proto.Message) {
	t.Helper()
	buf, err := io.ReadAll(body)
	require.NoError(t, err, "read body")
	require.NoError(t, protojson.Unmarshal(buf, msg), "protojson decode: %s", buf)
}

// ===== Happy-path coverage =====

func TestGetUser(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Decode the response into the proto type. Asserting on each field
	// pins the entire binding+method+serialization pipeline: if any one
	// of them breaks, this test fails with a precise diff.
	var user userpb.User
	decodeProto(t, resp.Body, &user)

	require.Equal(t, "1", user.Id, "path-param binding")
	require.Equal(t, "Alice", user.Name)
	require.Equal(t, userpb.Role_ROLE_ADMIN, user.Role)
}

func TestGetUser_NotFound(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users/999")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	// Default RFC 7807 envelope: Content-Type and structured body.
	require.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))

	var prob composition.ProblemDetails
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&prob))
	require.Equal(t, 404, prob.Status)
	require.Contains(t, prob.Detail, "999")
}

func TestListUsers_RoleFilter(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users?role=ROLE_ADMIN")
	require.NoError(t, err)
	defer resp.Body.Close()

	var page userpb.ListUsersResponse
	decodeProto(t, resp.Body, &page)

	require.Equal(t, int32(1), page.Total)
	require.Len(t, page.Users, 1)
	require.Equal(t, "Alice", page.Users[0].Name)
}

func TestListUsers_RoleFilter_NumericValueAlsoAccepted(t *testing.T) {
	srv := newTestServer(t)

	// PathEnum / QueryEnum accept both proto names and numeric values
	// per protojson conventions. ROLE_USER = 1.
	resp, err := http.Get(srv.URL + "/users?role=1")
	require.NoError(t, err)
	defer resp.Body.Close()

	var page userpb.ListUsersResponse
	decodeProto(t, resp.Body, &page)

	require.Equal(t, int32(2), page.Total, "expected 2 ROLE_USER users")
}

func TestListUsers_RoleFilter_InvalidEnumValue(t *testing.T) {
	srv := newTestServer(t)

	// Strict matching: "admin" is not a canonical proto name → 400.
	resp, err := http.Get(srv.URL + "/users?role=admin")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCreateUser_Returns201(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Post(srv.URL+"/users", "application/json",
		strings.NewReader(`{"name":"Dave","email":"d@example.com"}`))
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode, "OnSuccess was supposed to override default 200")

	var created userpb.User
	decodeProto(t, resp.Body, &created)
	require.Equal(t, "Dave", created.Name)
	require.Equal(t, "d@example.com", created.Email)
}

func TestUsersDTO_MapTransform(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users-dto/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	// Map renames Name → display_name and Email → contact. The result
	// is encoded via encoding/json (Map opts out of protojson), so
	// JSONEq is the natural assertion.
	buf, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.JSONEq(t,
		`{"id":"1", "display_name":"Alice", "contact":"alice@example.com"}`,
		string(buf),
	)
}

// ===== Catching binder / pattern mismatches =====

// TestGetUser_PinsPathParamBinding is the kind of test we recommend
// every Path/Query/Header binder ships with: not just a status-code
// check, but an assertion on the *value* that flowed through the
// binder. If somebody refactors the route from
//
//	mux.Handle("GET /users/{id}", composition.Proxy(users.GetUser,
//	    bind.PathString("id", func(req, v) { req.Id = v }),
//	))
//
// into the broken
//
//	mux.Handle("GET /users/{id}", composition.Proxy(users.GetUser,
//	    bind.PathString("hello", func(req, v) { req.Id = v }), // ← typo
//	))
//
// then [http.Request.PathValue]("hello") returns "" — the gRPC server
// sees an empty id and replies NotFound, so this test fails loudly.
//
// We do NOT have a startup-time check for this kind of mismatch (it's
// on the roadmap as a Binder-metadata refactor in v0.4+); for now, a
// pinning test like this is the cheapest tripwire.
func TestGetUser_PinsPathParamBinding(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/users/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode, "path-param binding likely broken")

	var user userpb.User
	decodeProto(t, resp.Body, &user)
	require.Equal(t, "1", user.Id, "path-param binding broken: req.Id was %q (likely wrong name in bind.PathString)", user.Id)
}

// TestDemonstrateBindMismatch is the negative version of the test
// above: it stands up a route with an intentionally wrong binder name
// and verifies the failure mode is the predictable NotFound (rather
// than a 500 / panic / silent success).
//
// The bug pattern, in real life:
//
//	mux.Handle("GET /flights/popular/{start_city_code}",
//	    composition.Proxy(aviaClient.GetPopular,
//	        bind.PathString("hello", func(req, v) { req.StartCityCode = v }),
//	    ))
//
// → r.PathValue("hello") = "" → empty request field → upstream returns
// "not found" / "empty argument" style error. This test demonstrates
// that the failure is visible and assertable from a black-box HTTP test.
func TestDemonstrateBindMismatch(t *testing.T) {
	client, cleanup := newInProcessClient()
	t.Cleanup(cleanup)

	mux := http.NewServeMux()
	// Intentional bug: pattern declares {id}, binder reads "hello".
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("hello", func(req *userpb.GetUserRequest, v string) { req.Id = v }),
	))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/users/1")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode,
		"misconfigured binder should yield 404 from empty-id lookup")

	// Verify the body confirms the empty-id lookup specifically — this
	// pins the diagnostic so future readers see what the failure looks
	// like, not just that *some* error occurred.
	var prob composition.ProblemDetails
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&prob))
	require.Contains(t, prob.Detail, `""`, "expected the empty id to appear in the error message")
}

// TestOpenAPIDocument checks the generated spec served at /openapi.json:
// every registered route is present, with parameters and schemas derived
// from the binders.
func TestOpenAPIDocument(t *testing.T) {
	srv := newTestServer(t)

	resp, err := http.Get(srv.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	var doc struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `json:"properties"`
			} `json:"schemas"`
		} `json:"components"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if doc.OpenAPI != "3.1.0" {
		t.Fatalf("openapi version: %q", doc.OpenAPI)
	}
	for _, want := range []string{"/users/{id}", "/users", "/users-dto/{id}"} {
		if _, ok := doc.Paths[want]; !ok {
			t.Fatalf("missing path %q in %v", want, doc.Paths)
		}
	}
	if got := doc.Paths["/users/{id}"]["get"].OperationID; got != "get-user" {
		t.Fatalf("operationId: %q", got)
	}
	user, ok := doc.Components.Schemas["userpb.User"]
	if !ok {
		t.Fatalf("missing User schema in %v", doc.Components.Schemas)
	}
	if _, ok := user.Properties["name"]; !ok {
		t.Fatalf("User properties: %v", user.Properties)
	}
}
