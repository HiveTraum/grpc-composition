// Runnable example: HTTP API in front of a unary gRPC user service.
//
// The gRPC server runs in-process via bufconn — no external services needed.
//
//	go run ./examples/basic
//	curl localhost:8080/users/1
//	curl 'localhost:8080/users?limit=1'
//	curl -X POST localhost:8080/users -d '{"name":"Carol","email":"carol@example.com"}'
//	curl localhost:8080/users-dto/1
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/traum-tech/grpc-composition"
	"github.com/traum-tech/grpc-composition/bind"
	"github.com/traum-tech/grpc-composition/examples/basic/userpb"
)

const bufSize = 1024 * 1024

// In-memory UserService implementation.
type userServer struct {
	userpb.UnimplementedUserServiceServer
	users []*userpb.User
}

func newUserServer() *userServer {
	return &userServer{
		users: []*userpb.User{
			{Id: "1", Name: "Alice", Email: "alice@example.com"},
			{Id: "2", Name: "Bob", Email: "bob@example.com"},
		},
	}
}

func (s *userServer) GetUser(_ context.Context, req *userpb.GetUserRequest) (*userpb.User, error) {
	for _, u := range s.users {
		if u.Id == req.Id {
			return u, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "user %q not found", req.Id)
}

func (s *userServer) ListUsers(_ context.Context, req *userpb.ListUsersRequest) (*userpb.ListUsersResponse, error) {
	users := s.users
	if off := int(req.Offset); off > 0 && off < len(users) {
		users = users[off:]
	}
	if lim := int(req.Limit); lim > 0 && lim < len(users) {
		users = users[:lim]
	}
	return &userpb.ListUsersResponse{Users: users, Total: int32(len(s.users))}, nil
}

func (s *userServer) CreateUser(_ context.Context, req *userpb.CreateUserRequest) (*userpb.User, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	u := &userpb.User{
		Id:    strconv.Itoa(len(s.users) + 1),
		Name:  req.Name,
		Email: req.Email,
	}
	s.users = append(s.users, u)
	return u, nil
}

func main() {
	// 1. In-process gRPC server.
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	userpb.RegisterUserServiceServer(srv, newUserServer())
	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Fatalf("grpc serve: %v", err)
		}
	}()
	defer srv.Stop()

	// 2. Dial the in-process server.
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	defer conn.Close()
	users := userpb.NewUserServiceClient(conn)

	// 3. Wire HTTP routes via grpc-composition.
	mux := http.NewServeMux()

	// GET /users/{id} — single path param, proto-by-default response.
	mux.Handle("GET /users/{id}", composition.Proxy(users.GetUser,
		bind.Path("id", func(req *userpb.GetUserRequest, v string) { req.Id = v }),
	))

	// GET /users?limit=10&offset=0 — two query params with parsing.
	mux.Handle("GET /users", composition.Proxy(users.ListUsers,
		bind.Query("limit", func(req *userpb.ListUsersRequest, v string) {
			if n, err := strconv.Atoi(v); err == nil {
				req.Limit = int32(n)
			}
		}),
		bind.Query("offset", func(req *userpb.ListUsersRequest, v string) {
			if n, err := strconv.Atoi(v); err == nil {
				req.Offset = int32(n)
			}
		}),
	))

	// POST /users — JSON body, returns 201 Created on success.
	mux.Handle("POST /users", composition.Proxy(users.CreateUser,
		bind.JSON[userpb.CreateUserRequest](),
	).OnSuccess(http.StatusCreated))

	// GET /users-dto/{id} — same upstream call, different REST shape via Map.
	mux.Handle("GET /users-dto/{id}", composition.Proxy(users.GetUser,
		bind.Path("id", func(req *userpb.GetUserRequest, v string) { req.Id = v }),
	).Map(func(u *userpb.User) any {
		return map[string]string{
			"id":           u.Id,
			"display_name": u.Name,
			"contact":      u.Email,
		}
	}))

	log.Println("listening on :8080")
	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}
