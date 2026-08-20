package composition_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/apipb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/HiveTraum/grpc-composition"
	"github.com/HiveTraum/grpc-composition/bind"
)

// ===== Mock gRPC client (stand-in for protoc-gen-go-grpc output). =====

type GetUserRequest struct {
	Id string
}

type GetUserResponse struct {
	Id   string
	Name string
}

type ListUsersRequest struct {
	Limit int32
}

type ListUsersResponse struct {
	Count int32
}

type SearchRequest struct {
	Page      int32
	Big       int64
	Published bool
}

type SearchResponse struct {
	Page      int32
	Big       int64
	Published bool
}

type mockClient struct {
	getUserErr error
}

func (c *mockClient) GetUser(_ context.Context, req *GetUserRequest, _ ...grpc.CallOption) (*GetUserResponse, error) {
	if c.getUserErr != nil {
		return nil, c.getUserErr
	}
	return &GetUserResponse{Id: req.Id, Name: "User-" + req.Id}, nil
}

func (c *mockClient) ListUsers(_ context.Context, req *ListUsersRequest, _ ...grpc.CallOption) (*ListUsersResponse, error) {
	return &ListUsersResponse{Count: req.Limit}, nil
}

func (c *mockClient) Search(_ context.Context, req *SearchRequest, _ ...grpc.CallOption) (*SearchResponse, error) {
	return &SearchResponse{Page: req.Page, Big: req.Big, Published: req.Published}, nil
}

// ===== Tests =====

func TestProxy_PathBinding(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.Path("id", func(req *GetUserRequest, v string) error { req.Id = v; return nil }),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	var body GetUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Id != "42" || body.Name != "User-42" {
		t.Fatalf("body: %+v", body)
	}
}

func TestProxy_QueryBinding(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users", composition.Proxy(client.ListUsers,
		bind.Query("limit", func(req *ListUsersRequest, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return err
			}
			req.Limit = int32(n)
			return nil
		}),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users?limit=10")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	var body ListUsersResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Count != 10 {
		t.Fatalf("count: got %d want 10", body.Count)
	}
}

func TestProxy_GRPCErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		code       codes.Code
		msg        string
		wantStatus int
		wantMsg    string
	}{
		{"NotFound", codes.NotFound, "no user", http.StatusNotFound, "no user"},
		{"InvalidArgument", codes.InvalidArgument, "bad id", http.StatusBadRequest, "bad id"},
		{"PermissionDenied", codes.PermissionDenied, "nope", http.StatusForbidden, "nope"},
		{"Unauthenticated", codes.Unauthenticated, "who", http.StatusUnauthorized, "who"},
		{"AlreadyExists", codes.AlreadyExists, "dup", http.StatusConflict, "dup"},
		{"FailedPrecondition", codes.FailedPrecondition, "precond", http.StatusUnprocessableEntity, "precond"},
		{"ResourceExhausted", codes.ResourceExhausted, "rate", http.StatusTooManyRequests, "rate"},
		{"Aborted", codes.Aborted, "abort", http.StatusConflict, "abort"},
		{"Unavailable", codes.Unavailable, "leak this!", http.StatusServiceUnavailable, "internal error"},
		{"Internal", codes.Internal, "leak this!", http.StatusInternalServerError, "internal error"},
		{"Unknown", codes.Unknown, "leak this!", http.StatusInternalServerError, "internal error"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &mockClient{getUserErr: status.Error(tc.code, tc.msg)}

			mux := http.NewServeMux()
			mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
				bind.Path("id", func(req *GetUserRequest, v string) error { req.Id = v; return nil }),
			))

			srv := httptest.NewServer(mux)
			defer srv.Close()

			resp, err := http.Get(srv.URL + "/users/42")
			if err != nil {
				t.Fatalf("http: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d want %d", resp.StatusCode, tc.wantStatus)
			}

			var body composition.ProblemDetails
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Detail != tc.wantMsg {
				t.Fatalf("detail: got %q want %q", body.Detail, tc.wantMsg)
			}
			if body.Status != tc.wantStatus {
				t.Fatalf("body.status: got %d want %d", body.Status, tc.wantStatus)
			}
		})
	}
}

// ===== protojson serialization path =====

type wrapperClient struct{}

func (c *wrapperClient) Echo(_ context.Context, req *wrapperspb.StringValue, _ ...grpc.CallOption) (*wrapperspb.StringValue, error) {
	return &wrapperspb.StringValue{Value: "echo:" + req.Value}, nil
}

func TestProxy_ProtoJSONOutput(t *testing.T) {
	client := &wrapperClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /echo/{v}", composition.Proxy(client.Echo,
		bind.Path("v", func(req *wrapperspb.StringValue, v string) error { req.Value = v; return nil }),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/echo/hello")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	// protojson serializes wrapperspb.StringValue as a bare JSON string,
	// not as an object. This proves we are using protojson, not encoding/json.
	want := `"echo:hello"`
	if got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
}

// ===== Body binders (BodyJSON / BodyJSONInto / BodyJSONMap / Body) =====

type EchoDTO struct {
	Text string `json:"text"`
}

func TestBodyJSONInto(t *testing.T) {
	client := &wrapperClient{}
	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.Echo,
		bind.BodyJSONInto(func(dto EchoDTO, req *wrapperspb.StringValue) error {
			if dto.Text == "" {
				return fmt.Errorf("text required")
			}
			req.Value = strings.ToUpper(dto.Text)
			return nil
		}),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("ok", func(t *testing.T) {
		resp, _ := http.Post(srv.URL+"/echo", "application/json",
			strings.NewReader(`{"text":"hello"}`))
		defer resp.Body.Close()
		buf, _ := io.ReadAll(resp.Body)
		got := strings.TrimSpace(string(buf))
		if got != `"echo:HELLO"` {
			t.Fatalf("body: got %s", got)
		}
	})

	t.Run("apply error → 400", func(t *testing.T) {
		resp, _ := http.Post(srv.URL+"/echo", "application/json",
			strings.NewReader(`{"text":""}`))
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
		var body composition.ProblemDetails
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if !strings.Contains(body.Detail, "text required") {
			t.Fatalf("detail: got %q", body.Detail)
		}
	})
}

func TestBodyJSONMap(t *testing.T) {
	client := &wrapperClient{}
	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.Echo,
		bind.BodyJSONMap(func(dto EchoDTO, req *wrapperspb.StringValue) {
			req.Value = strings.ToUpper(dto.Text)
		}),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/echo", "application/json",
		strings.NewReader(`{"text":"hello"}`))
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	if got != `"echo:HELLO"` {
		t.Fatalf("body: got %s", got)
	}
}

func TestBody_CustomParse(t *testing.T) {
	client := &wrapperClient{}
	mux := http.NewServeMux()
	mux.Handle("POST /raw", composition.Proxy(client.Echo,
		// Treat raw body as the string value directly — demonstrates
		// the generic escape hatch for non-JSON formats.
		bind.Body(func(data []byte, req *wrapperspb.StringValue) error {
			req.Value = string(data)
			return nil
		}),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/raw", "text/plain",
		strings.NewReader("plain text body"))
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	if got != `"echo:plain text body"` {
		t.Fatalf("body: got %s", got)
	}
}

// ===== BodyJSON (proto-direct via protojson) =====

func TestBodyJSON(t *testing.T) {
	client := &wrapperClient{}

	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.Echo,
		bind.BodyJSON[wrapperspb.StringValue](),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// protojson encodes StringValue as a bare JSON string literal.
	resp, err := http.Post(srv.URL+"/echo", "application/json", strings.NewReader(`"world"`))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	want := `"echo:world"`
	if got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
}

// ===== Map (response transformation) =====

type UserDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func TestProxy_Map(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.Path("id", func(req *GetUserRequest, v string) error { req.Id = v; return nil }),
	).Map(func(resp *GetUserResponse) any {
		return UserDTO{ID: resp.Id, DisplayName: "Mr. " + resp.Name}
	}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	var body UserDTO
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != "42" || body.DisplayName != "Mr. User-42" {
		t.Fatalf("body: %+v", body)
	}
}

// ===== OnSuccess (custom HTTP status) =====

func TestProxy_OnSuccess(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.Path("id", func(req *GetUserRequest, v string) error { req.Id = v; return nil }),
	).OnSuccess(http.StatusCreated))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d want 201", resp.StatusCode)
	}
}

// ===== Typed sugar helpers (PathString/QueryString, PathInt32/64, PathBool, QueryInt32/64, QueryBool, PathAs, QueryAs) =====

func TestPathString(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/abc-42")
	defer resp.Body.Close()
	var body GetUserResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Id != "abc-42" {
		t.Fatalf("id: got %q want %q", body.Id, "abc-42")
	}
}

func TestQueryString(t *testing.T) {
	type NameReq struct{ Name string }
	type NameResp struct{ Name string }
	echo := func(_ context.Context, req *NameReq, _ ...grpc.CallOption) (*NameResp, error) {
		return &NameResp{Name: req.Name}, nil
	}

	mux := http.NewServeMux()
	mux.Handle("GET /search", composition.Proxy(echo,
		bind.QueryString("q", func(req *NameReq, v string) { req.Name = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("present", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search?q=alice")
		defer resp.Body.Close()
		var body NameResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Name != "alice" {
			t.Fatalf("name: got %q want %q", body.Name, "alice")
		}
	})

	t.Run("absent → empty string", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search")
		defer resp.Body.Close()
		var body NameResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Name != "" {
			t.Fatalf("name: got %q want empty", body.Name)
		}
	})
}

func TestQueryInt32(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /search", composition.Proxy(client.Search,
		bind.QueryInt32("page", func(req *SearchRequest, v int32) { req.Page = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("ok", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search?page=7")
		defer resp.Body.Close()
		var body SearchResponse
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Page != 7 {
			t.Fatalf("page: got %d want 7", body.Page)
		}
	})

	t.Run("empty leaves zero", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search")
		defer resp.Body.Close()
		var body SearchResponse
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Page != 0 {
			t.Fatalf("page: got %d want 0 (default)", body.Page)
		}
	})

	t.Run("bad input → 400 with prefix", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search?page=oops")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
		var body composition.ProblemDetails
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if !strings.Contains(body.Detail, "page") {
			t.Fatalf("detail: %q does not contain %q", body.Detail, "page")
		}
	})
}

func TestPathInt64_Bool(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /search/{big}/{pub}", composition.Proxy(client.Search,
		bind.PathInt64("big", func(req *SearchRequest, v int64) { req.Big = v }),
		bind.PathBool("pub", func(req *SearchRequest, v bool) { req.Published = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("ok", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search/9999999999/true")
		defer resp.Body.Close()
		var body SearchResponse
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Big != 9999999999 || body.Published != true {
			t.Fatalf("body: %+v", body)
		}
	})

	t.Run("bad bool → 400", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/search/1/maybe")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})
}

// ===== Enum binders =====

// LabelReq / LabelResp use descriptorpb.FieldDescriptorProto_Label as a real
// generated protobuf enum (values: LABEL_OPTIONAL=1, LABEL_REQUIRED=2,
// LABEL_REPEATED=3) so we can exercise PathEnum / QueryEnum without
// shipping our own test-only .proto.
type LabelReq struct {
	L descriptorpb.FieldDescriptorProto_Label
}
type LabelResp struct {
	L descriptorpb.FieldDescriptorProto_Label
}

type labelClient struct{}

func (c *labelClient) Echo(_ context.Context, req *LabelReq, _ ...grpc.CallOption) (*LabelResp, error) {
	return &LabelResp{L: req.L}, nil
}

func TestPathEnum(t *testing.T) {
	client := &labelClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /labels/{l}", composition.Proxy(client.Echo,
		bind.PathEnum("l", func(req *LabelReq, v descriptorpb.FieldDescriptorProto_Label) { req.L = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("by name", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels/LABEL_REQUIRED")
		defer resp.Body.Close()
		var body LabelResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.L != descriptorpb.FieldDescriptorProto_LABEL_REQUIRED {
			t.Fatalf("got %v want LABEL_REQUIRED", body.L)
		}
	})

	t.Run("by number", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels/3") // LABEL_REPEATED
		defer resp.Body.Close()
		var body LabelResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.L != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			t.Fatalf("got %v want LABEL_REPEATED", body.L)
		}
	})

	t.Run("unknown name → 400", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels/NOPE")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})

	t.Run("unknown number → 400", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels/99")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})

	t.Run("case-sensitive: lowercase rejected", func(t *testing.T) {
		// Verifies strict matching: "label_required" is not a valid proto name.
		resp, _ := http.Get(srv.URL + "/labels/label_required")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400 (case-sensitive matching)", resp.StatusCode)
		}
	})
}

func TestQueryEnum(t *testing.T) {
	client := &labelClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /labels", composition.Proxy(client.Echo,
		bind.QueryEnum("l", func(req *LabelReq, v descriptorpb.FieldDescriptorProto_Label) { req.L = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("present by name", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels?l=LABEL_OPTIONAL")
		defer resp.Body.Close()
		var body LabelResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.L != descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL {
			t.Fatalf("got %v want LABEL_OPTIONAL", body.L)
		}
	})

	t.Run("absent → zero value", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels")
		defer resp.Body.Close()
		var body LabelResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		// Zero of the type — there is no enum value with number 0 in this
		// enum, but the int32 zero value is what we expect with empty query.
		if int32(body.L) != 0 {
			t.Fatalf("got %v want zero", body.L)
		}
	})

	t.Run("bad value → 400", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/labels?l=NOPE")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})
}

func TestPathAs_CustomParser(t *testing.T) {
	client := &mockClient{}
	parseHex := func(s string) (int32, error) {
		n, err := strconv.ParseInt(s, 16, 32)
		return int32(n), err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /hex/{v}", composition.Proxy(client.Search,
		bind.PathAs("v", parseHex, func(req *SearchRequest, v int32) { req.Page = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/hex/ff")
	defer resp.Body.Close()
	var body SearchResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Page != 255 {
		t.Fatalf("page: got %d want 255", body.Page)
	}
}

// ===== Binder error propagation =====

// When a setter returns an error (e.g. parse failure), the framework must
// short-circuit, skip the gRPC call, and return HTTP 400 with the message.
func TestProxy_BinderError(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users", composition.Proxy(client.ListUsers,
		bind.Query("limit", func(req *ListUsersRequest, v string) error {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("limit: %w", err)
			}
			req.Limit = int32(n)
			return nil
		}),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users?limit=notanumber")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}

	var body composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Detail, "limit") {
		t.Fatalf("detail: got %q want one containing %q", body.Detail, "limit")
	}
}

// ===== Custom path extractor (chi compatibility hook) =====

func TestSetDefaultPathExtractor(t *testing.T) {
	// Replace the extractor so it ignores the actual URL and returns a
	// fixed value. This proves bind.Path goes through PathExtractor, not
	// directly through r.PathValue.
	composition.SetDefaultPathExtractor(func(_ *http.Request, name string) string {
		return "extracted-" + name
	})
	defer composition.SetDefaultPathExtractor(nil) // restore stdlib

	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.Path("id", func(req *GetUserRequest, v string) error { req.Id = v; return nil }),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/ignored")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	var body GetUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Despite the URL containing "ignored", the bound Id reflects the
	// custom extractor's output: "extracted-id".
	if body.Id != "extracted-id" {
		t.Fatalf("id: got %q want %q", body.Id, "extracted-id")
	}
}

// ===== RFC 7807 error details + WithErrorMapper =====

func TestRFC7807_BadRequest(t *testing.T) {
	// Server returns InvalidArgument with two field violations,
	// the way protovalidate or a hand-rolled interceptor would.
	st, err := status.New(codes.InvalidArgument, "validation failed").WithDetails(
		&errdetails.BadRequest{
			FieldViolations: []*errdetails.BadRequest_FieldViolation{
				{Field: "user.email", Description: "must be a valid email"},
				{Field: "user.age", Description: "must be >= 18"},
			},
		},
	)
	if err != nil {
		t.Fatalf("withDetails: %v", err)
	}
	client := &mockClient{getUserErr: st.Err()}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/42")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type: got %q want application/problem+json", ct)
	}

	var body composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != 400 || body.Title != "Bad Request" || body.Detail != "validation failed" {
		t.Fatalf("envelope: %+v", body)
	}
	if len(body.Errors) != 2 {
		t.Fatalf("errors: got %d want 2 — %+v", len(body.Errors), body.Errors)
	}
	if body.Errors[0].Field != "user.email" || body.Errors[1].Field != "user.age" {
		t.Fatalf("field violations: %+v", body.Errors)
	}
}

func TestRFC7807_ErrorInfo(t *testing.T) {
	st, err := status.New(codes.ResourceExhausted, "too many requests").WithDetails(
		&errdetails.ErrorInfo{
			Reason:   "RATE_LIMIT_EXCEEDED",
			Domain:   "billing.example.com",
			Metadata: map[string]string{"retry_after": "60"},
		},
	)
	if err != nil {
		t.Fatalf("withDetails: %v", err)
	}
	client := &mockClient{getUserErr: st.Err()}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/42")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status: got %d want 429", resp.StatusCode)
	}

	var body composition.ProblemDetails
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Reason != "RATE_LIMIT_EXCEEDED" {
		t.Fatalf("reason: got %q", body.Reason)
	}
	if body.Type != "billing.example.com/RATE_LIMIT_EXCEEDED" {
		t.Fatalf("type: got %q", body.Type)
	}
	if body.Metadata["retry_after"] != "60" {
		t.Fatalf("metadata: %+v", body.Metadata)
	}
}

func TestRFC7807_5xx_RedactsDetails(t *testing.T) {
	// Server returns Internal with details — both message AND details
	// must be redacted, never reaching the client.
	st, err := status.New(codes.Internal, "sql: password rejected by host db-master-3").WithDetails(
		&errdetails.ErrorInfo{Reason: "DB_PASSWORD_INVALID", Domain: "internal"},
	)
	if err != nil {
		t.Fatalf("withDetails: %v", err)
	}
	client := &mockClient{getUserErr: st.Err()}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/42")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500", resp.StatusCode)
	}

	var body composition.ProblemDetails
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.Detail != "internal error" {
		t.Fatalf("detail must be redacted: got %q", body.Detail)
	}
	if body.Reason != "" || body.Type != "" {
		t.Fatalf("details must be redacted: %+v", body)
	}
	if strings.Contains(body.Detail, "password") || strings.Contains(body.Detail, "db-master-3") {
		t.Fatalf("upstream secrets leaked into 5xx body: %q", body.Detail)
	}
}

func TestWithErrorMapper_PerRoute(t *testing.T) {
	client := &mockClient{getUserErr: status.Error(codes.NotFound, "no user")}

	// Per-route mapper that produces a totally different shape (e.g. legacy API).
	legacy := func(err error) (int, any) {
		return 418, map[string]string{"legacy_code": "TEAPOT", "msg": err.Error()}
	}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).WithErrorMapper(legacy))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/42")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status: got %d want 418 (per-route mapper)", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: got %q want application/json (non-Problem body)", ct)
	}

	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["legacy_code"] != "TEAPOT" {
		t.Fatalf("body: %+v", body)
	}
}

func TestSetDefaultErrorMapper(t *testing.T) {
	composition.SetDefaultErrorMapper(func(err error) (int, any) {
		return 599, map[string]string{"custom": "yes", "msg": err.Error()}
	})
	defer composition.SetDefaultErrorMapper(nil) // restore RFC 7807 default

	client := &mockClient{getUserErr: status.Error(codes.NotFound, "x")}
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/users/42")
	defer resp.Body.Close()
	if resp.StatusCode != 599 {
		t.Fatalf("status: got %d want 599 (custom default)", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["custom"] != "yes" {
		t.Fatalf("body: %+v", body)
	}
}

// ===== App + metadata forwarding =====

// captureClient stores the outgoing gRPC metadata seen at invocation
// time, so tests can assert which headers got forwarded.
type captureClient struct {
	captured metadata.MD
}

func (c *captureClient) Echo(ctx context.Context, req *wrapperspb.StringValue, _ ...grpc.CallOption) (*wrapperspb.StringValue, error) {
	md, _ := metadata.FromOutgoingContext(ctx)
	c.captured = md.Copy()
	return &wrapperspb.StringValue{Value: req.Value}, nil
}

func TestApp_MetadataForward(t *testing.T) {
	app := composition.New(
		composition.WithMetadataForward("Authorization", "X-Request-ID"),
	)

	client := &captureClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /echo/{v}", composition.Proxy(client.Echo,
		bind.PathString("v", func(req *wrapperspb.StringValue, v string) { req.Value = v }),
	))
	srv := httptest.NewServer(app.Handler(mux))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/echo/hi", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Request-ID", "req-42")
	req.Header.Set("X-Not-Forwarded", "should-not-leak")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if got := client.captured.Get("authorization"); len(got) != 1 || got[0] != "Bearer secret-token" {
		t.Fatalf("authorization: %v", got)
	}
	if got := client.captured.Get("x-request-id"); len(got) != 1 || got[0] != "req-42" {
		t.Fatalf("x-request-id: %v", got)
	}
	if got := client.captured.Get("x-not-forwarded"); len(got) != 0 {
		t.Fatalf("x-not-forwarded must NOT be forwarded: %v", got)
	}
}

func TestApp_NoForwarding_PassThrough(t *testing.T) {
	// App with no forwarding should return inner handler unchanged
	// and not add any outgoing metadata.
	app := composition.New()

	client := &captureClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /echo/{v}", composition.Proxy(client.Echo,
		bind.PathString("v", func(req *wrapperspb.StringValue, v string) { req.Value = v }),
	))
	srv := httptest.NewServer(app.Handler(mux))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/echo/hi", nil)
	req.Header.Set("Authorization", "Bearer x")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if len(client.captured) != 0 {
		t.Fatalf("expected empty outgoing md, got %v", client.captured)
	}
}

func TestApp_MetadataForward_MultipleValues(t *testing.T) {
	// Multi-value headers (Accept-Language: en, ru) preserve all values.
	app := composition.New(composition.WithMetadataForward("Accept-Language"))

	client := &captureClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /echo/{v}", composition.Proxy(client.Echo,
		bind.PathString("v", func(req *wrapperspb.StringValue, v string) { req.Value = v }),
	))
	srv := httptest.NewServer(app.Handler(mux))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/echo/hi", nil)
	req.Header.Add("Accept-Language", "en")
	req.Header.Add("Accept-Language", "ru")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	if got := client.captured.Get("accept-language"); len(got) != 2 || got[0] != "en" || got[1] != "ru" {
		t.Fatalf("accept-language: %v", got)
	}
}

// ===== Header binders =====

type HeaderReq struct {
	TenantID string
	Limit    int32
	Strict   bool
	Ratio    float64
}
type HeaderResp struct {
	TenantID string
	Limit    int32
	Strict   bool
	Ratio    float64
}

type headerClient struct{}

func (c *headerClient) Echo(_ context.Context, req *HeaderReq, _ ...grpc.CallOption) (*HeaderResp, error) {
	return &HeaderResp{TenantID: req.TenantID, Limit: req.Limit, Strict: req.Strict, Ratio: req.Ratio}, nil
}

func TestHeaderString(t *testing.T) {
	client := &headerClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /h", composition.Proxy(client.Echo,
		bind.HeaderString("X-Tenant-ID", func(req *HeaderReq, v string) { req.TenantID = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/h", nil)
	req.Header.Set("X-Tenant-ID", "acme-co")
	resp, _ := http.DefaultClient.Do(req)
	defer resp.Body.Close()

	var body HeaderResp
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body.TenantID != "acme-co" {
		t.Fatalf("tenant_id: %q", body.TenantID)
	}
}

func TestHeader_RequiredMissing(t *testing.T) {
	client := &headerClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /h", composition.Proxy(client.Echo,
		bind.Header("X-Tenant-ID", func(req *HeaderReq, v string) error {
			if v == "" {
				return fmt.Errorf("X-Tenant-ID is required")
			}
			req.TenantID = v
			return nil
		}),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/h")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	var body composition.ProblemDetails
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if !strings.Contains(body.Detail, "X-Tenant-ID") {
		t.Fatalf("detail: %q", body.Detail)
	}
}

func TestHeaderInt32(t *testing.T) {
	client := &headerClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /h", composition.Proxy(client.Echo,
		bind.HeaderInt32("X-Rate-Limit", func(req *HeaderReq, v int32) { req.Limit = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("present", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/h", nil)
		req.Header.Set("X-Rate-Limit", "100")
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		var body HeaderResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Limit != 100 {
			t.Fatalf("limit: %d", body.Limit)
		}
	})

	t.Run("absent → zero", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/h")
		defer resp.Body.Close()
		var body HeaderResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Limit != 0 {
			t.Fatalf("limit: got %d want 0", body.Limit)
		}
	})

	t.Run("bad value → 400", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/h", nil)
		req.Header.Set("X-Rate-Limit", "oops")
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})
}

func TestPathFloat64_QueryFloat64(t *testing.T) {
	client := &headerClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /scale/{ratio}", composition.Proxy(client.Echo,
		bind.PathFloat64("ratio", func(req *HeaderReq, v float64) { req.Ratio = v }),
	))
	mux.Handle("GET /q", composition.Proxy(client.Echo,
		bind.QueryFloat64("ratio", func(req *HeaderReq, v float64) { req.Ratio = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("PathFloat64 ok", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/scale/0.5")
		defer resp.Body.Close()
		var body HeaderResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Ratio != 0.5 {
			t.Fatalf("ratio: %v", body.Ratio)
		}
	})

	t.Run("PathFloat64 bad → 400", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/scale/oops")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d", resp.StatusCode)
		}
	})

	t.Run("QueryFloat64 absent", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/q")
		defer resp.Body.Close()
		var body HeaderResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Ratio != 0 {
			t.Fatalf("ratio: %v want 0", body.Ratio)
		}
	})

	t.Run("QueryFloat64 present", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/q?ratio=3.14")
		defer resp.Body.Close()
		var body HeaderResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Ratio != 3.14 {
			t.Fatalf("ratio: %v", body.Ratio)
		}
	})
}

// ===== Aggregate handler =====

func TestAggregate_ReturnsProtoMessage(t *testing.T) {
	// Aggregator that returns a proto message — should serialize via
	// protojson and 200 by default.
	mux := http.NewServeMux()
	mux.Handle("GET /agg", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return &wrapperspb.StringValue{Value: "from-aggregate"}, nil
		},
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/agg")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	// protojson encodes StringValue as a bare JSON string literal.
	if got != `"from-aggregate"` {
		t.Fatalf("body: got %s", got)
	}
}

func TestAggregate_ReturnsCustomStruct(t *testing.T) {
	type FeedResponse struct {
		User  string   `json:"user"`
		Posts []string `json:"posts"`
	}

	mux := http.NewServeMux()
	mux.Handle("GET /feed", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return FeedResponse{User: "alice", Posts: []string{"p1", "p2"}}, nil
		},
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/feed")
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(buf))
	const want = `{"user":"alice","posts":["p1","p2"]}`
	if got != want {
		t.Fatalf("body: got %s want %s", got, want)
	}
}

func TestAggregate_OnSuccess(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("POST /resources", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return map[string]string{"id": "42"}, nil
		},
	).OnSuccess(http.StatusCreated))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Post(srv.URL+"/resources", "application/json", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status: got %d want 201", resp.StatusCode)
	}
}

func TestAggregate_GRPCError(t *testing.T) {
	// Aggregator returns a gRPC error — should hit DefaultErrorMapper
	// (RFC 7807) and produce application/problem+json.
	mux := http.NewServeMux()
	mux.Handle("GET /agg", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return nil, status.Error(codes.NotFound, "thing missing")
		},
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/agg")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type: %q", ct)
	}
	var prob composition.ProblemDetails
	_ = json.NewDecoder(resp.Body).Decode(&prob)
	if prob.Detail != "thing missing" {
		t.Fatalf("detail: %q", prob.Detail)
	}
}

func TestAggregate_NonGRPCErrorMappedToInternal(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("GET /agg", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return nil, fmt.Errorf("something bad happened with credentials") // plain error
		},
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/agg")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500", resp.StatusCode)
	}
	var prob composition.ProblemDetails
	_ = json.NewDecoder(resp.Body).Decode(&prob)
	// 5xx redacts upstream detail — plain error message must NOT leak.
	if strings.Contains(prob.Detail, "credentials") {
		t.Fatalf("upstream detail leaked into 5xx: %q", prob.Detail)
	}
}

func TestAggregate_WithErrorMapper(t *testing.T) {
	legacy := func(err error) (int, any) {
		return 418, map[string]string{"legacy": "yes"}
	}
	mux := http.NewServeMux()
	mux.Handle("GET /agg", composition.Aggregate(
		func(ctx context.Context, r *http.Request) (any, error) {
			return nil, status.Error(codes.NotFound, "x")
		},
	).WithErrorMapper(legacy))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/agg")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status: got %d want 418", resp.StatusCode)
	}
}

// ===== HeaderEnum =====

func TestHeaderEnum(t *testing.T) {
	type RoleReq struct {
		R descriptorpb.FieldDescriptorProto_Label
	}
	type RoleResp struct {
		R descriptorpb.FieldDescriptorProto_Label
	}
	echo := func(_ context.Context, req *RoleReq, _ ...grpc.CallOption) (*RoleResp, error) {
		return &RoleResp{R: req.R}, nil
	}

	mux := http.NewServeMux()
	mux.Handle("GET /e", composition.Proxy(echo,
		bind.HeaderEnum("X-Label", func(req *RoleReq, v descriptorpb.FieldDescriptorProto_Label) { req.R = v }),
	))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("present by name", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/e", nil)
		req.Header.Set("X-Label", "LABEL_REPEATED")
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		var body RoleResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.R != descriptorpb.FieldDescriptorProto_LABEL_REPEATED {
			t.Fatalf("label: got %v", body.R)
		}
	})

	t.Run("present by number", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/e", nil)
		req.Header.Set("X-Label", "2") // LABEL_REQUIRED
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		var body RoleResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.R != descriptorpb.FieldDescriptorProto_LABEL_REQUIRED {
			t.Fatalf("label: got %v", body.R)
		}
	})

	t.Run("absent → zero", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/e")
		defer resp.Body.Close()
		var body RoleResp
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if int32(body.R) != 0 {
			t.Fatalf("label: got %v want zero", body.R)
		}
	})

	t.Run("invalid → 400", func(t *testing.T) {
		req, _ := http.NewRequest("GET", srv.URL+"/e", nil)
		req.Header.Set("X-Label", "label_required") // lowercase rejected
		resp, _ := http.DefaultClient.Do(req)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status: got %d want 400", resp.StatusCode)
		}
	})
}

// ===== App as router (generic methods on a non-generic type) =====

func TestApp_VerbMethods(t *testing.T) {
	client := &mockClient{}

	app := composition.New()
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	)
	app.Post("/users", client.ListUsers,
		bind.BodyJSONMap(func(dto struct {
			Limit int32 `json:"limit"`
		}, req *ListUsersRequest) {
			req.Limit = dto.Limit
		}),
	).OnSuccess(http.StatusCreated)
	app.Delete("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	)

	srv := httptest.NewServer(app)
	defer srv.Close()

	t.Run("get", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/users/42")
		if err != nil {
			t.Fatalf("http: %v", err)
		}
		defer resp.Body.Close()
		var body GetUserResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Id != "42" || body.Name != "User-42" {
			t.Fatalf("body: %+v", body)
		}
	})

	t.Run("post honours OnSuccess", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/users", "application/json",
			strings.NewReader(`{"limit":7}`))
		if err != nil {
			t.Fatalf("http: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status: got %d want 201", resp.StatusCode)
		}
		var body ListUsersResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Count != 7 {
			t.Fatalf("body: %+v", body)
		}
	})

	t.Run("verb is part of the pattern", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/users/42", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("http: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("status: got %d want 405 (PUT not registered)", resp.StatusCode)
		}
	})
}

func TestApp_HandleAggregate(t *testing.T) {
	app := composition.New()
	app.Handle("GET /feed", composition.Aggregate(
		func(_ context.Context, _ *http.Request) (any, error) {
			return map[string]int{"items": 2}, nil
		},
	))

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/feed")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]int
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["items"] != 2 {
		t.Fatalf("body: %+v", body)
	}
}

func TestApp_ServeHTTP_ForwardsMetadata(t *testing.T) {
	client := &captureClient{}

	app := composition.New(composition.WithMetadataForward("Authorization"))
	app.Get("/echo/{v}", client.Echo,
		bind.PathString("v", func(req *wrapperspb.StringValue, v string) { req.Value = v }),
	)

	srv := httptest.NewServer(app)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/echo/hi", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("X-Not-Forwarded", "should-not-leak")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if got := client.captured.Get("authorization"); len(got) != 1 || got[0] != "Bearer secret-token" {
		t.Fatalf("authorization: %v", got)
	}
	if got := client.captured.Get("x-not-forwarded"); len(got) != 0 {
		t.Fatalf("x-not-forwarded must NOT be forwarded: %v", got)
	}
}

// ===== Typed callbacks enabled by generic methods =====

func TestProxy_Map_TypedDTO(t *testing.T) {
	client := &mockClient{}

	app := composition.New()
	// The transformer returns UserDTO, not any: Out is inferred from it.
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).Map(func(resp *GetUserResponse) UserDTO {
		return UserDTO{ID: resp.Id, DisplayName: "Mr. " + resp.Name}
	})

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	var body UserDTO
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != "42" || body.DisplayName != "Mr. User-42" {
		t.Fatalf("body: %+v", body)
	}
}

func TestWithErrorMapper_TypedBody(t *testing.T) {
	type LegacyError struct {
		LegacyCode string `json:"legacy_code"`
		Msg        string `json:"msg"`
	}

	client := &mockClient{getUserErr: status.Error(codes.NotFound, "no user")}

	app := composition.New()
	// Body is inferred as LegacyError — the mapper needs no any.
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).WithErrorMapper(func(err error) (int, LegacyError) {
		return http.StatusTeapot, LegacyError{LegacyCode: "TEAPOT", Msg: err.Error()}
	})

	srv := httptest.NewServer(app)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/users/42")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("status: got %d want 418", resp.StatusCode)
	}
	var body LegacyError
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.LegacyCode != "TEAPOT" {
		t.Fatalf("body: %+v", body)
	}
}

// An ErrorMapper value still satisfies the generic mapper methods
// (Body = any), so pre-existing mappers keep compiling.
func TestWithErrorMapper_AcceptsErrorMapperValue(t *testing.T) {
	var mapper composition.ErrorMapper = func(error) (int, any) {
		return http.StatusTeapot, map[string]string{"legacy_code": "TEAPOT"}
	}

	client := &mockClient{getUserErr: status.Error(codes.NotFound, "no user")}

	app := composition.New()
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).WithErrorMapper(mapper)
	app.Handle("GET /feed", composition.Aggregate(
		func(_ context.Context, _ *http.Request) (any, error) {
			return nil, status.Error(codes.NotFound, "no feed")
		},
	).WithErrorMapper(mapper))

	srv := httptest.NewServer(app)
	defer srv.Close()

	for _, path := range []string{"/users/42", "/feed"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("http %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusTeapot {
			t.Fatalf("%s status: got %d want 418", path, resp.StatusCode)
		}
	}
}

// ===== protojson marshal options =====

// apiClient returns an empty proto3 message (apipb.Api), so the difference
// between default marshaling ({}) and EmitUnpopulated (zero-value fields
// present) is observable without codegen.
type apiClient struct{}

func (c *apiClient) Get(_ context.Context, _ *GetUserRequest, _ ...grpc.CallOption) (*apipb.Api, error) {
	return &apipb.Api{}, nil
}

func fetchJSONObject(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func TestSetDefaultMarshalOptions_EmitUnpopulated(t *testing.T) {
	t.Cleanup(func() { composition.SetDefaultMarshalOptions(protojson.MarshalOptions{}) })

	client := &apiClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /api", composition.Proxy(client.Get))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	if body := fetchJSONObject(t, srv.URL+"/api"); len(body) != 0 {
		t.Fatalf("default marshal: want empty object, got %v", body)
	}

	composition.SetDefaultMarshalOptions(protojson.MarshalOptions{EmitUnpopulated: true})

	body := fetchJSONObject(t, srv.URL+"/api")
	if _, ok := body["name"]; !ok {
		t.Fatalf("EmitUnpopulated: want zero-value fields present, got %v", body)
	}
}

func TestWithMarshalOptions_PerRoute(t *testing.T) {
	client := &apiClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /default", composition.Proxy(client.Get))
	mux.Handle("GET /emit", composition.Proxy(client.Get).
		WithMarshalOptions(protojson.MarshalOptions{EmitUnpopulated: true}))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	if body := fetchJSONObject(t, srv.URL+"/default"); len(body) != 0 {
		t.Fatalf("default route: want empty object, got %v", body)
	}
	if body := fetchJSONObject(t, srv.URL+"/emit"); len(body) == 0 {
		t.Fatal("per-route EmitUnpopulated: want zero-value fields present, got empty object")
	}
}

// ===== MapReasons =====

func reasonServer(t *testing.T, callErr error, table map[string]composition.ReasonMapping) *httptest.Server {
	t.Helper()
	client := &mockClient{getUserErr: callErr}
	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).WithErrorMapper(composition.MapReasons(table, nil)))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestMapReasons_MatchedReason(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "upstream message").WithDetails(
		&errdetails.ErrorInfo{Reason: "NAME_TAKEN", Domain: "example.com"},
	)
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	srv := reasonServer(t, st.Err(), map[string]composition.ReasonMapping{
		"NAME_TAKEN": {Status: http.StatusConflict, Detail: "name already taken"},
	})

	resp, err := http.Get(srv.URL + "/users/1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status: got %d want 409", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type: got %q", ct)
	}
	var prob composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prob.Detail != "name already taken" {
		t.Fatalf("detail: got %q", prob.Detail)
	}
	if prob.Reason != "NAME_TAKEN" || prob.Type != "example.com/NAME_TAKEN" {
		t.Fatalf("reason/type: got %q / %q", prob.Reason, prob.Type)
	}
}

func TestMapReasons_UpstreamDetailWhenEmpty(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "upstream message").WithDetails(
		&errdetails.ErrorInfo{Reason: "REPO_HOST_DOWN", Domain: "example.com"},
	)
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	srv := reasonServer(t, st.Err(), map[string]composition.ReasonMapping{
		"REPO_HOST_DOWN": {Status: http.StatusServiceUnavailable},
	})

	resp, err := http.Get(srv.URL + "/users/1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", resp.StatusCode)
	}
	var prob composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 5xx with no declared detail: free text redacted, structured members kept.
	if prob.Detail != "internal error" {
		t.Fatalf("detail: got %q want redacted", prob.Detail)
	}
	if prob.Reason != "REPO_HOST_DOWN" {
		t.Fatalf("reason: got %q", prob.Reason)
	}
}

func TestMapReasons_FallbackOnUnknownReason(t *testing.T) {
	st, err := status.New(codes.FailedPrecondition, "upstream message").WithDetails(
		&errdetails.ErrorInfo{Reason: "SOMETHING_ELSE", Domain: "example.com"},
	)
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	srv := reasonServer(t, st.Err(), map[string]composition.ReasonMapping{
		"NAME_TAKEN": {Status: http.StatusConflict},
	})

	resp, err := http.Get(srv.URL + "/users/1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	// Falls through to the default mapper: FailedPrecondition → 422.
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d want 422", resp.StatusCode)
	}
	var prob composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prob.Detail != "upstream message" {
		t.Fatalf("detail: got %q", prob.Detail)
	}
}

func TestMapReasons_FallbackOnPlainError(t *testing.T) {
	srv := reasonServer(t, fmt.Errorf("boom"), map[string]composition.ReasonMapping{
		"NAME_TAKEN": {Status: http.StatusConflict},
	})

	resp, err := http.Get(srv.URL + "/users/1")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: got %d want 500", resp.StatusCode)
	}
}

// ===== bind.Ctx =====

type identityKey struct{}

func TestBindCtx(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /me", composition.Proxy(client.GetUser,
		bind.Ctx(
			func(ctx context.Context) (string, error) {
				v, ok := ctx.Value(identityKey{}).(string)
				if !ok {
					return "", fmt.Errorf("no identity")
				}
				return v, nil
			},
			func(req *GetUserRequest, v string) { req.Id = v },
		),
	))

	// Simulated auth middleware: puts the identity into the context.
	withIdentity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get("X-Test-Identity"); id != "" {
			r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		}
		mux.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(withIdentity)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/me", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-Test-Identity", "org-7")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	var body GetUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Id != "org-7" {
		t.Fatalf("body: %+v", body)
	}
}

func TestBindCtx_GetError(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /me", composition.Proxy(client.GetUser,
		bind.Ctx(
			func(_ context.Context) (string, error) { return "", fmt.Errorf("no identity") },
			func(req *GetUserRequest, v string) { req.Id = v },
		),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/me")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", resp.StatusCode)
	}
	var prob composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prob.Detail != "bind: context: no identity" {
		t.Fatalf("detail: got %q", prob.Detail)
	}
}

// ===== Body size limit =====

func TestBodyLimit(t *testing.T) {
	bind.SetDefaultMaxBodyBytes(16)
	t.Cleanup(func() { bind.SetDefaultMaxBodyBytes(10 << 20) })

	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.GetUser,
		bind.Body(func(data []byte, req *GetUserRequest) error {
			req.Id = string(data)
			return nil
		}),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/echo", "text/plain", strings.NewReader("short"))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("small body status: got %d want 200", resp.StatusCode)
	}

	resp, err = http.Post(srv.URL+"/echo", "text/plain", strings.NewReader(strings.Repeat("x", 64)))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status: got %d want 413", resp.StatusCode)
	}
	var prob composition.ProblemDetails
	if err := json.NewDecoder(resp.Body).Decode(&prob); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if prob.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("problem status: got %d", prob.Status)
	}
}

func TestBodyLimit_Disabled(t *testing.T) {
	bind.SetDefaultMaxBodyBytes(0)
	t.Cleanup(func() { bind.SetDefaultMaxBodyBytes(10 << 20) })

	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.GetUser,
		bind.Body(func(data []byte, req *GetUserRequest) error {
			req.Id = string(data)
			return nil
		}),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/echo", "text/plain", strings.NewReader(strings.Repeat("x", 1<<20)))
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status with disabled limit: got %d want 200", resp.StatusCode)
	}
}

// ===== Binder interface / App registration =====

func TestBinderFunc_CustomBinder(t *testing.T) {
	client := &mockClient{}
	mux := http.NewServeMux()
	mux.Handle("GET /custom", composition.Proxy(client.GetUser,
		composition.BinderFunc[GetUserRequest](func(_ *http.Request, req *GetUserRequest) error {
			req.Id = "custom"
			return nil
		}),
	))

	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/custom")
	if err != nil {
		t.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()

	var body GetUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Id != "custom" {
		t.Fatalf("body: %+v", body)
	}
}

func TestApp_PathBinderMismatch_PanicsAtRegistration(t *testing.T) {
	client := &mockClient{}
	app := composition.New()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic: path binder name absent from the pattern")
		}
	}()
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("nope", func(req *GetUserRequest, v string) { req.Id = v }),
	)
}

func TestApp_Operations(t *testing.T) {
	client := &mockClient{}
	app := composition.New()
	app.Get("/users/{id}", client.GetUser,
		bind.PathString("id", func(req *GetUserRequest, v string) { req.Id = v }),
	).Doc(composition.Doc{OperationID: "get-user"})
	app.Handle("GET /opaque", http.NotFoundHandler()) // not a proxy — excluded

	ops := app.Operations()
	if len(ops) != 1 {
		t.Fatalf("operations: %+v", ops)
	}
	op := ops[0]
	if op.Method != http.MethodGet || op.Pattern != "/users/{id}" || op.Doc.OperationID != "get-user" {
		t.Fatalf("operation: %+v", op)
	}
	if len(op.Params) != 1 || op.Params[0].In != composition.InPath || op.Params[0].Name != "id" || !op.Params[0].Required {
		t.Fatalf("params: %+v", op.Params)
	}
}
