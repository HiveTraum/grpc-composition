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
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strconv"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/HiveTraum/grpc-composition"
	"github.com/HiveTraum/grpc-composition/bind"
	"github.com/HiveTraum/grpc-composition/examples/basic/userpb"
	"github.com/HiveTraum/grpc-composition/openapi"
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
			{Id: "1", Name: "Alice", Email: "alice@example.com", Role: userpb.Role_ROLE_ADMIN},
			{Id: "2", Name: "Bob", Email: "bob@example.com", Role: userpb.Role_ROLE_USER},
			{Id: "3", Name: "Carol", Email: "carol@example.com", Role: userpb.Role_ROLE_USER},
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
	if req.Role != userpb.Role_ROLE_UNSPECIFIED {
		filtered := users[:0:0]
		for _, u := range users {
			if u.Role == req.Role {
				filtered = append(filtered, u)
			}
		}
		users = filtered
	}
	if off := int(req.Offset); off > 0 && off < len(users) {
		users = users[off:]
	}
	if lim := int(req.Limit); lim > 0 && lim < len(users) {
		users = users[:lim]
	}
	return &userpb.ListUsersResponse{Users: users, Total: int32(len(users))}, nil
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

// newInProcessClient starts an in-process gRPC server backed by the
// in-memory userServer and returns a client connected to it via bufconn.
// Factored out of newApp so tests can build alternative HTTP wirings
// (e.g. a route with an intentionally wrong binder name) over the same
// gRPC backend.
func newInProcessClient() (userpb.UserServiceClient, func()) {
	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer()
	userpb.RegisterUserServiceServer(grpcSrv, newUserServer())
	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			log.Printf("grpc serve: %v", err)
		}
	}()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("grpc dial: %v", err)
	}
	return userpb.NewUserServiceClient(conn), func() {
		_ = conn.Close()
		grpcSrv.Stop()
	}
}

// newApp wires the example end-to-end: starts an in-process gRPC server
// over bufconn, dials it, and registers HTTP routes via grpc-composition.
// Returns the HTTP handler and a cleanup function. Exposed (instead of
// inlined into main) so the tests in main_test.go can drive the full
// stack via httptest without duplicating the wiring.
func newApp() (http.Handler, func()) {
	users, cleanup := newInProcessClient()

	app := composition.New()

	// GET /users/{id} — single path param, proto-by-default response.
	// Doc(...) is optional and purely declarative — it only enriches the
	// generated OpenAPI document.
	app.Get("/users/{id}", users.GetUser,
		bind.PathString("id", func(req *userpb.GetUserRequest, v string) { req.Id = v }),
	).Doc(composition.Doc{OperationID: "get-user", Summary: "Get a user by id", Tags: []string{"users"}})

	// GET /users?limit=10&offset=0&role=ROLE_USER — int32 + enum query params.
	// Empty values are tolerated; bad parse → HTTP 400 with "<param>:" prefix.
	app.Get("/users", users.ListUsers,
		bind.QueryInt32("limit", func(req *userpb.ListUsersRequest, v int32) { req.Limit = v }),
		bind.QueryInt32("offset", func(req *userpb.ListUsersRequest, v int32) { req.Offset = v }),
		bind.QueryEnum("role", func(req *userpb.ListUsersRequest, v userpb.Role) { req.Role = v }),
	)

	// POST /users — JSON body, returns 201 Created on success.
	app.Post("/users", users.CreateUser,
		bind.BodyJSON[userpb.CreateUserRequest](),
	).OnSuccess(http.StatusCreated)

	// GET /users-dto/{id} — same upstream call, different REST shape via Map.
	// The transformer keeps its own return type; Map infers it.
	app.Get("/users-dto/{id}", users.GetUser,
		bind.PathString("id", func(req *userpb.GetUserRequest, v string) { req.Id = v }),
	).Map(func(u *userpb.User) UserDTO {
		return UserDTO{ID: u.Id, DisplayName: u.Name, Contact: u.Email}
	})

	// GET /openapi.json — the OpenAPI 3.1 document generated from the
	// binder metadata of the routes above. Generate runs once, after all
	// routes are registered.
	doc := openapi.Generate(app, openapi.Info{Title: "basic example", Version: "0.1.0"})
	spec, err := json.Marshal(doc)
	if err != nil {
		log.Fatalf("openapi: %v", err)
	}
	app.Handle("GET /openapi.json", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(spec)
	}))

	return app, cleanup
}

// UserDTO is the REST-facing shape served by /users-dto/{id}.
type UserDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Contact     string `json:"contact"`
}

func main() {
	handler, cleanup := newApp()
	defer cleanup()

	log.Println("listening on :8080")
	if err := http.ListenAndServe(":8080", handler); err != nil {
		log.Fatal(err)
	}
}
