package main

import (
	"context"
	"log"
	"net/http"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/improbable-eng/grpc-web/go/grpcweb"

	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

func main() {
	s := &server{
		groups:                  syncmap.New[string, *pb.Group](),
		groupsSubscribers:       syncmap.New[string, chan *pb.GroupsUpdate](),
		applications:            syncmap.New[string, []*pb.GroupApplication](),
		applicationsSubscribers: syncmap.New[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]](),
	}

	r := keyResolver{}
	authFunc := func(ctx context.Context) (context.Context, error) {
		token, err := grpc_auth.AuthFromMD(ctx, "bearer")
		if err != nil {
			return nil, err
		}

		clientId, err := r.Resolve(ctx, token)
		if err != nil {
			return nil, err
		}

		if clientId == "" {
			return nil, status.Errorf(codes.Unauthenticated, "invalid auth token")
		}

		return withClientInfo(ctx, &clientInfo{
			AccountID: clientId,
			Token:     token,
		}), nil
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(grpc_auth.UnaryServerInterceptor(authFunc)),
		grpc.ChainStreamInterceptor((grpc_auth.StreamServerInterceptor(authFunc))),
	)

	pb.RegisterLfgServiceServer(grpcServer, s)
	reflection.Register(grpcServer)
	wrappedGrpc := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(func(origin string) bool {
			// Allow all origins for development
			return true
		}),
		grpcweb.WithAllowedRequestHeaders([]string{"*"}),
	)

	// Start HTTP server
	server := &http.Server{
		Addr:    "127.0.0.1:5001",
		Handler: wrappedGrpc,
	}

	log.Printf("Starting server on %s", server.Addr)
	log.Printf("Listing the following resources: %v", grpcweb.ListGRPCResources(grpcServer))
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
