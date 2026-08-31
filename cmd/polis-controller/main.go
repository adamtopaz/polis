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
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	controllermanager "sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	polisv1alpha1 "github.com/adamtopaz/polis/api/v1alpha1"
	"github.com/adamtopaz/polis/internal/api"
	poliscontroller "github.com/adamtopaz/polis/internal/controller"
	"github.com/adamtopaz/polis/internal/store"
	"github.com/adamtopaz/polis/internal/token"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "polis-controller:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("polis-controller", flag.ContinueOnError)
	listen := flags.String("listen", env("POLIS_LISTEN", ":8080"), "HTTP listen address")
	dbPath := flags.String("db", env("POLIS_DB_PATH", "./polis.db"), "database path")
	operatorTokenFile := flags.String("operator-token-file", env("POLIS_OPERATOR_TOKEN_FILE", ""), "path to the operator token")
	workerTokenFile := flags.String("worker-token-file", env("POLIS_WORKER_TOKEN_FILE", ""), "path to the worker token")
	kubernetesNamespace := flags.String("kubernetes-namespace", env("POLIS_KUBERNETES_NAMESPACE", ""), "namespace to watch (empty watches all namespaces)")
	healthProbeAddress := flags.String("health-probe-bind-address", env("POLIS_HEALTH_PROBE_ADDRESS", ":8081"), "controller-runtime health probe address")
	metricsAddress := flags.String("metrics-bind-address", env("POLIS_METRICS_ADDRESS", "0"), "controller-runtime metrics address; 0 disables metrics")
	leaderElection := flags.Bool("leader-elect", envBool("POLIS_LEADER_ELECT", false), "enable Kubernetes leader election")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("polis-controller takes no positional arguments")
	}
	operatorToken, err := token.Load("POLIS_OPERATOR_TOKEN", *operatorTokenFile, "operator")
	if err != nil {
		return err
	}
	workerToken, err := token.Load("POLIS_WORKER_TOKEN", *workerTokenFile, "worker")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	database, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer database.Close()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(polisv1alpha1.AddToScheme(scheme))
	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: *metricsAddress},
		HealthProbeBindAddress: *healthProbeAddress,
		LeaderElection:         *leaderElection,
		LeaderElectionID:       "polis-controller.polis.dev",
	}
	if *kubernetesNamespace != "" {
		options.Cache = cache.Options{DefaultNamespaces: map[string]cache.Config{*kubernetesNamespace: {}}}
	}
	configuration, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("load Kubernetes configuration: %w", err)
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	manager, err := ctrl.NewManager(configuration, options)
	if err != nil {
		return fmt.Errorf("create Kubernetes manager: %w", err)
	}
	if err := (&poliscontroller.AgentReconciler{
		Client: manager.GetClient(), Scheme: manager.GetScheme(), Store: database,
	}).SetupWithManager(manager); err != nil {
		return fmt.Errorf("create Agent controller: %w", err)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readiness check: %w", err)
	}
	server := &http.Server{
		Addr:              *listen,
		Handler:           api.New(database, logger, operatorToken, workerToken).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := manager.Add(controllermanager.RunnableFunc(func(ctx context.Context) error {
		result := make(chan error, 1)
		go func() {
			logger.Info("controller listening", "address", *listen, "database", *dbPath)
			result <- server.ListenAndServe()
		}()
		select {
		case err := <-result:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-result; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})); err != nil {
		return fmt.Errorf("add HTTP server: %w", err)
	}
	logger.Info("Kubernetes manager started", "namespace", *kubernetesNamespace)
	return manager.Start(ctx)
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true"
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
