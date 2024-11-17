package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/improbable-eng/grpc-web/go/grpcweb"

	pb "gw2lfgserver/pb"
	"gw2lfgserver/syncmap"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Config struct {
	Port               int
	MetricsPort        int
	MaxConnAge         time.Duration
	KeepAliveTime      time.Duration
	KeepAliveTimeout   time.Duration
	MaxRecvMsgSize     int
	MaxSendMsgSize     int
	MaxConcurrentConns int
}

func loadConfig() (*Config, error) {
	// Get PORT from Render environment (required)
	port := os.Getenv("PORT")
	if port == "" {
		return nil, fmt.Errorf("PORT environment variable is required")
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("invalid PORT value: %w", err)
	}

	// Get optional configs with defaults
	metricsPort := 9090
	if mp := os.Getenv("METRICS_PORT"); mp != "" {
		if mpNum, err := strconv.Atoi(mp); err == nil {
			metricsPort = mpNum
		}
	}

	maxConns := 1000
	if mc := os.Getenv("MAX_CONN_COUNT"); mc != "" {
		if mcNum, err := strconv.Atoi(mc); err == nil {
			maxConns = mcNum
		}
	}

	return &Config{
		Port:               portNum,
		MetricsPort:        metricsPort,
		MaxConnAge:         time.Hour,
		KeepAliveTime:      time.Minute * 2,
		KeepAliveTimeout:   time.Second * 20,
		MaxRecvMsgSize:     4 * 1024 * 1024, // 4MB
		MaxSendMsgSize:     4 * 1024 * 1024, // 4MB
		MaxConcurrentConns: maxConns,
	}, nil
}

func setupHealthCheck(grpcServer *grpc.Server) {
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
}

func main() {
	// Load configuration
	config, err := loadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Configure keepalive parameters
	kasp := keepalive.ServerParameters{
		MaxConnectionAge: config.MaxConnAge,
		Time:             config.KeepAliveTime,
		Timeout:          config.KeepAliveTimeout,
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

	// Initialize gRPC server
	grpcServer := grpc.NewServer(
		grpc.KeepaliveParams(kasp),
		grpc.MaxRecvMsgSize(config.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(config.MaxSendMsgSize),
		grpc.MaxConcurrentStreams(uint32(config.MaxConcurrentConns)),
		grpc.ChainUnaryInterceptor(grpc_auth.UnaryServerInterceptor(authFunc)),
		grpc.ChainStreamInterceptor((grpc_auth.StreamServerInterceptor(authFunc))),
	)

	// Create and register the LFG service
	server := &Server{
		groups:                  syncmap.New[string, *pb.Group](),
		groupsSubscribers:       syncmap.New[string, chan *pb.GroupsUpdate](),
		applications:            syncmap.New[string, []*pb.GroupApplication](),
		applicationsSubscribers: syncmap.New[string, *syncmap.Map[string, chan *pb.GroupApplicationUpdate]](),
	}
	pb.RegisterLfgServiceServer(grpcServer, server)

	// Setup health check required by Render
	setupHealthCheck(grpcServer)

	// Enable reflection in development
	if os.Getenv("ENABLE_REFLECTION") == "true" {
		reflection.Register(grpcServer)
	}

	// Start metrics server if enabled
	if config.MetricsPort > 0 {
		go func() {
			if err := startMetricsServer(config.MetricsPort); err != nil {
				log.Printf("Failed to start metrics server: %v", err)
			}
		}()
	}

	wrappedGrpc := grpcweb.WrapServer(grpcServer,
		grpcweb.WithOriginFunc(func(origin string) bool {
			// Allow all origins for development
			return true
		}),
		grpcweb.WithAllowedRequestHeaders([]string{"*"}),
	)

	// Start HTTP server
	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Port),
		Handler: wrappedGrpc,
	}

	log.Printf("Starting server on %s", httpServer.Addr)
	log.Printf("Listing the following resources: %v", grpcweb.ListGRPCResources(grpcServer))
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func startMetricsServer(port int) error {
	// Implement metrics server (optional)
	return nil
}
