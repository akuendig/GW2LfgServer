package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	grpc_auth "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/auth"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	grpc_ratelimit "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/ratelimit"
	"github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/improbable-eng/grpc-web/go/grpcweb"

	"gw2lfgserver/authenticator"
	"gw2lfgserver/database"
	"gw2lfgserver/keyresolver"
	"gw2lfgserver/kpme"
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
	Host                string
	Port                int
	MetricsPort         int
	MaxConnAge          time.Duration
	KeepAliveTime       time.Duration
	KeepAliveTimeout    time.Duration
	MaxRecvMsgSize      int
	MaxSendMsgSize      int
	MaxConcurrentConns  int
	DatabasePath        string
	DatabaseEntryTTL    time.Duration
	DatabaseCleanupFreq time.Duration
}

func loadConfig() (*Config, error) {
	// Get PORT from Render environment (required)
	port := 50051
	if p := os.Getenv("PORT"); p != "" {
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

	dbEntryTTL := 2 * time.Hour
	if ttl := os.Getenv("DATABASE_ENTRY_TTL"); ttl != "" {
		ttlDuration, err := time.ParseDuration(ttl)
		if err != nil {
			return nil, fmt.Errorf("invalid DATABASE_ENTRY_TTL value: %w", err)
		}
		dbEntryTTL = ttlDuration
	} else {
		slog.Warn("DATABASE_ENTRY_TTL environment variable not set, using default value 1h")
	}

	dbCleanupFreq := 5 * time.Minute
	if cf := os.Getenv("DATABASE_CLEANUP_FREQ"); cf != "" {
		cfDuration, err := time.ParseDuration(cf)
		if err != nil {
			return nil, fmt.Errorf("invalid DATABASE_CLEANUP_FREQ value: %w", err)
		}
		dbCleanupFreq = cfDuration
	} else {
		slog.Warn("DATABASE_CLEANUP_FREQ environment variable not set, using default value 5m")
	}

	return &Config{
		Host:                host,
		Port:                port,
		MetricsPort:         metricsPort,
		MaxConnAge:          time.Hour,
		KeepAliveTime:       time.Hour,
		KeepAliveTimeout:    time.Second * 20,
		MaxRecvMsgSize:      4 * 1024 * 1024, // 4MB
		MaxSendMsgSize:      4 * 1024 * 1024, // 4MB
		MaxConcurrentConns:  maxConns,
		DatabasePath:        dbPath,
		DatabaseEntryTTL:    dbEntryTTL,
		DatabaseCleanupFreq: dbCleanupFreq,
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
	kpClient := kpme.NewClient()
	// Create unary/stream rateLimiters, based on token bucket here.
	// You can implement your own rate-limiter for the interface.
	limiter := ratelimit.NewRateLimiter(ratelimit.Config{
		RequestsPerSecond: 1,
		Burst:             20,
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
	server := NewServer(db, kpClient)
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
		}()
	}

	// Setup signal handling
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if config.DatabaseEntryTTL > 0 && config.DatabaseCleanupFreq > 0 {
		go func() {
			server.CleanUpExpiredDatabaseEntries(ctx, config.DatabaseEntryTTL, config.DatabaseCleanupFreq)
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
	requestCounter := new(int64)
	httpServer := &http.Server{
		Addr: fmt.Sprintf("%s:%d", config.Host, config.Port),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			// Increment request counter
			counter := atomic.AddInt64(requestCounter, 1)
			slog.Info("HTTP request", "seq", counter, "method", r.Method, "url", r.URL.String())
			wrappedGrpc.ServeHTTP(w, r)
			slog.Info("HTTP response", "seq", counter, "duration", time.Since(start))
		}),
	}

	// Channel to receive shutdown signal
	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)

	// Channel to receive error from HTTP server
	serverError := make(chan error, 1)

	// Start HTTP server in a goroutine
	go func() {
		slog.Info(
			"Starting server",
			"address", httpServer.Addr,
			"resources", grpcweb.ListGRPCResources(grpcServer))
		if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
			serverError <- err
		}
	}()

	// Wait for shutdown signal or server error
	select {
	case <-shutdown:
		slog.Info("Shutdown signal received")
	case err := <-serverError:
		slog.Error("Server error", "error", err)
	}

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Initiate graceful shutdown
	slog.Info("Initiating graceful shutdown")

	// Shutdown HTTP server first
	if err := httpServer.Shutdown(ctx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	}

	// Stop gRPC server gracefully
	grpcServer.GracefulStop()

	// Close database connection
	if err := db.Close(); err != nil {
		slog.Error("Database closure error", "error", err)
	}

	slog.Info("Server shutdown complete")
}
