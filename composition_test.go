package composition_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/traum-tech/grpc-composition"
	"github.com/traum-tech/grpc-composition/bind"
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

// ===== Tests =====

func TestProxy_PathBinding(t *testing.T) {
	client := &mockClient{}

	mux := http.NewServeMux()
	mux.Handle("GET /users/{id}", composition.Proxy(client.GetUser,
		bind.Path("id", func(req *GetUserRequest, v string) { req.Id = v }),
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
		bind.Query("limit", func(req *ListUsersRequest, v string) {
			n, _ := strconv.Atoi(v)
			req.Limit = int32(n)
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
				bind.Path("id", func(req *GetUserRequest, v string) { req.Id = v }),
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
		bind.Path("v", func(req *wrapperspb.StringValue, v string) { req.Value = v }),
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
