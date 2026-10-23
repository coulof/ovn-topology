package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/coulof/ovn-topology/pkg/graph"
	"github.com/coulof/ovn-topology/pkg/ovsdb"
	"github.com/coulof/ovn-topology/pkg/server"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// Version metadata set via ldflags at build time
	version   = "0.1.0-dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listenAddr  = getEnvDefault("LISTEN_ADDR", ":8080")
		nbEndpoints = getEnvDefault("OVN_NB_ENDPOINTS", "tcp:127.0.0.1:6641")
		sbEndpoints = getEnvDefault("OVN_SB_ENDPOINTS", "tcp:127.0.0.1:6642")
		certFile    = getEnvDefault("OVN_CERT", "")
		keyFile     = getEnvDefault("OVN_KEY", "")
		caFile      = getEnvDefault("OVN_CA", "")
		logLevelStr = getEnvDefault("LOG_LEVEL", "info")
		kubeconfig  = getEnvDefault("KUBECONFIG", "")
	)

	flag.StringVar(&listenAddr, "listen-addr", listenAddr, "HTTP service listen address")
	flag.StringVar(&nbEndpoints, "nb-endpoints", nbEndpoints, "Comma-separated OVN Northbound OVSDB endpoints")
	flag.StringVar(&sbEndpoints, "sb-endpoints", sbEndpoints, "Comma-separated OVN Southbound OVSDB endpoints")
	flag.StringVar(&certFile, "ovn-cert", certFile, "Path to OVN client certificate file")
	flag.StringVar(&keyFile, "ovn-key", keyFile, "Path to OVN client private key file")
	flag.StringVar(&caFile, "ovn-ca", caFile, "Path to OVN CA certificate file")
	flag.StringVar(&logLevelStr, "log-level", logLevelStr, "Log level (debug, info, warn, error)")
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "Path to kubeconfig file (optional, defaults to in-cluster)")
	flag.Parse()

	// Configure structured logger
	var level slog.Level
	switch strings.ToLower(logLevelStr) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	logger.Info("starting ovn-topologyd",
		"version", version,
		"commit", gitCommit,
		"build_date", buildDate,
		"listen_addr", listenAddr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Initialize Kubernetes NodeMapper and WorkloadResolver (read-only per hard rule 2)
	nodeMapper, workloadResolver, watchKube := setupKubeMappers(kubeconfig, logger)

	// Create thread-safe in-memory graph store
	store := graph.NewStore(nodeMapper, workloadResolver)
	if watchKube != nil {
		go watchKube(ctx, store)
	}

	// Parse endpoints
	nbList := splitEndpoints(nbEndpoints)
	sbList := splitEndpoints(sbEndpoints)

	nbConfig := ovsdb.Config{
		Endpoints:        nbList,
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		InactivityCheck:  30 * time.Second,
		ReconnectTimeout: 10 * time.Second,
	}

	sbConfig := ovsdb.Config{
		Endpoints:        sbList,
		CertFile:         certFile,
		KeyFile:          keyFile,
		CAFile:           caFile,
		InactivityCheck:  30 * time.Second,
		ReconnectTimeout: 10 * time.Second,
	}

	nbClient, err := ovsdb.NewNBClient(ctx, nbConfig)
	if err != nil {
		return fmt.Errorf("failed to create NB client: %w", err)
	}

	sbClient, err := ovsdb.NewSBClient(ctx, sbConfig)
	if err != nil {
		return fmt.Errorf("failed to create SB client: %w", err)
	}

	syncer := ovsdb.NewSyncer(nbClient, sbClient, store, logger)

	// Start OVSDB syncer in background
	go func() {
		if err := syncer.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("syncer error", "error", err)
		}
	}()

	// Build version metadata for HTTP API
	versionConfig := server.VersionConfig{
		Version:            version,
		APIVersion:         "v1",
		OVNNbSchemaVersion: "7.3.0",
		OVNSbSchemaVersion: "20.29.0",
		GitCommit:          gitCommit,
		BuildDate:          buildDate,
	}

	srv := server.NewServer(store, versionConfig, logger)
	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0, // Disabled for SSE streaming
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", listenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down ovn-topologyd")
	case err := <-serverErr:
		return fmt.Errorf("http server failed: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	syncer.Close()
	return httpServer.Shutdown(shutdownCtx)
}

// setupKubeMappers returns the node mapper and workload resolver, and, with a
// Kubernetes client, the watch that keeps them current (nil otherwise).
func setupKubeMappers(kubeconfig string, logger *slog.Logger) (graph.NodeMapper, graph.WorkloadResolver, func(context.Context, *graph.Store)) {
	var k8sConfig *rest.Config
	var err error

	if kubeconfig != "" {
		k8sConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		k8sConfig, err = rest.InClusterConfig()
	}

	if err != nil {
		logger.Info("not running inside kubernetes or kubeconfig not provided; using static hostname mapping")
		return &graph.StaticNodeMapper{}, &graph.StaticWorkloadResolver{}, nil
	}

	clientset, err := kubernetes.NewForConfig(k8sConfig)
	if err != nil {
		logger.Warn("failed to initialize kubernetes client", "error", err)
		return &graph.StaticNodeMapper{}, &graph.StaticWorkloadResolver{}, nil
	}

	mapper := graph.NewKubeNodeMapper(clientset, logger)
	workloadResolver := graph.NewKubeWorkloadResolver(clientset, logger)
	watch := func(ctx context.Context, store *graph.Store) {
		graph.WatchKubernetes(ctx, clientset, mapper, workloadResolver, store, 2*time.Second, logger)
	}
	return mapper, workloadResolver, watch
}

func splitEndpoints(s string) []string {
	parts := strings.Split(s, ",")
	var res []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func getEnvDefault(key, def string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return def
}
