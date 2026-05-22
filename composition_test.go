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

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/descriptorpb"
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

			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body["error"] != tc.wantMsg {
				t.Fatalf("error msg: got %q want %q", body["error"], tc.wantMsg)
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

// ===== JSON body binding =====

func TestProxy_JSONBody(t *testing.T) {
	client := &wrapperClient{}

	mux := http.NewServeMux()
	mux.Handle("POST /echo", composition.Proxy(client.Echo,
		bind.JSON[wrapperspb.StringValue](),
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
		var body map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if !strings.Contains(body["error"], "page") {
			t.Fatalf("error: %q does not contain %q", body["error"], "page")
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

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body["error"], "limit") {
		t.Fatalf("error: got %q want one containing %q", body["error"], "limit")
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
