package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	grpc_ratelimit "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/ratelimit"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/improbable-eng/grpc-web/go/grpcweb"

	"gw2lfgserver/authenticator"
	"gw2lfgserver/database"
	"gw2lfgserver/keyresolver"
	pb "gw2lfgserver/pb"
	"gw2lfgserver/ratelimit"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type Config struct {
	Host               string
	Port               int
	MetricsPort        int
	MaxConnAge         time.Duration
	KeepAliveTime      time.Duration
	KeepAliveTimeout   time.Duration
	MaxRecvMsgSize     int
	MaxSendMsgSize     int
	MaxConcurrentConns int
	DatabasePath       string
}

func loadConfig() (*Config, error) {
	// Get PORT from Render environment (required)
	port := 50051
	if p := os.Getenv("METRICS_PORT"); p != "" {
		portNum, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT value: %w", err)
		}
		port = portNum
	} else {
		slog.Warn("PORT environment variable not set, using default port 50051")
	}

	host := "127.0.0.1"
	if h := os.Getenv("RENDER"); h != "" {
		host = ""
	}

	// Get optional configs with defaults
	metricsPort := 9090
	if mp := os.Getenv("METRICS_PORT"); mp != "" {
		portNum, err := strconv.Atoi(mp)
		if err != nil {
			return nil, fmt.Errorf("invalid PORT value: %w", err)
		}
		metricsPort = portNum
	} else {
		slog.Warn("METRICS_PORT environment variable not set, metrics server will not be started")
	}

	maxConns := 1000
	if mc := os.Getenv("MAX_CONN_COUNT"); mc != "" {
		if mcNum, err := strconv.Atoi(mc); err == nil {
			maxConns = mcNum
		}
	} else {
		slog.Warn("MAX_CONN_COUNT environment variable not set, using default value 1000")
	}

	// dbPath := "file::memory:?cache=shared"
	dbPath := "./gw2lfg.db"
	if dp := os.Getenv("DATABASE_PATH"); dp != "" {
		dbPath = dp
	} else {
		slog.Warn("DATABASE_PATH environment variable not set, using in-memory database")
	}

	return &Config{
		Host:               host,
		Port:               port,
		MetricsPort:        metricsPort,
		MaxConnAge:         time.Hour,
		KeepAliveTime:      time.Hour,
		KeepAliveTimeout:   time.Second * 20,
		MaxRecvMsgSize:     4 * 1024 * 1024, // 4MB
		MaxSendMsgSize:     4 * 1024 * 1024, // 4MB
		MaxConcurrentConns: maxConns,
		DatabasePath:       dbPath,
	}, nil
}

func setupHealthCheck(grpcServer *grpc.Server) {
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
}

// InterceptorLogger adapts slog logger to interceptor logger.
// This code is simple enough to be copied and not imported.
func InterceptorLogger(l *slog.Logger) logging.Logger {
	return logging.LoggerFunc(func(ctx context.Context, lvl logging.Level, msg string, fields ...any) {
		l.Log(ctx, slog.Level(lvl), msg, fields...)
	})
}

func main() {
	// Load configuration
	config, err := loadConfig()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		return
	}

	// Initialize database
	db, err := database.New(database.Config{Path: config.DatabasePath})
	if err != nil {
		slog.Error("Failed to initialize database", "error", err)
		return
	}

	// Configure keepalive parameters
	kasp := keepalive.ServerParameters{
		MaxConnectionIdle: config.KeepAliveTime,
		MaxConnectionAge:  config.MaxConnAge,
		Time:              config.KeepAliveTime,
		Timeout:           config.KeepAliveTimeout,
	}

	keyResolver := keyresolver.New()
	authenticator := authenticator.New(keyResolver)
	// Create unary/stream rateLimiters, based on token bucket here.
	// You can implement your own rate-limiter for the interface.
	limiter := ratelimit.NewRateLimiter(ratelimit.Config{
		RequestsPerSecond: 1,
		Burst:             10,
		CleanupInterval:   time.Minute,
	})

	loggingOpts := []logging.Option{
		logging.WithLogOnEvents(logging.StartCall, logging.FinishCall),
		// Add any other option (check functions starting with logging.With).
	}
	recoveryOpts := []recovery.Option{
		recovery.WithRecoveryHandler(func(p interface{}) error {
			slog.Error("panic recovered", "error", p)
			return status.Errorf(codes.Internal, "internal server error")
		}),
	}
	// Initialize gRPC server
	// TODO: Also use proto validate?
	grpcServer := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 5 * time.Second}),
		grpc.KeepaliveParams(kasp),
		grpc.MaxRecvMsgSize(config.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(config.MaxSendMsgSize),
		grpc.MaxConcurrentStreams(uint32(config.MaxConcurrentConns)),
		grpc.ChainUnaryInterceptor(
			logging.UnaryServerInterceptor(InterceptorLogger(slog.Default()), loggingOpts...),
			grpc_auth.UnaryServerInterceptor(authenticator.Authenticate),
			grpc_ratelimit.UnaryServerInterceptor(limiter),
			recovery.UnaryServerInterceptor(recoveryOpts...),
		),
		grpc.ChainStreamInterceptor(
			logging.StreamServerInterceptor(InterceptorLogger(slog.Default()), loggingOpts...),
			grpc_auth.StreamServerInterceptor(authenticator.Authenticate),
			grpc_ratelimit.StreamServerInterceptor(limiter),
			recovery.StreamServerInterceptor(recoveryOpts...),
		),
	)

	// Create and register the LFG service
	server := NewServer(db)
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
				slog.Warn("Failed to start metrics server", "error", err)
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
		Addr:    fmt.Sprintf("%s:%d", config.Host, config.Port),
		Handler: wrappedGrpc,
	}

	slog.Info(
		"Starting server",
		"address", httpServer.Addr,
		"resources", grpcweb.ListGRPCResources(grpcServer))
	if err := httpServer.ListenAndServe(); err != nil {
		slog.Error("failed to serve: %v", "error", err)
	}
}

func startMetricsServer(port int) error {
	// Implement metrics server (optional)
	return nil
}
