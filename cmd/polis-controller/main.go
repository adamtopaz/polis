package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	polisv1alpha1 "github.com/adamtopaz/polis/api/v1alpha1"
	poliscontroller "github.com/adamtopaz/polis/internal/controller"
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
		Client: manager.GetClient(), Scheme: manager.GetScheme(),
	}).SetupWithManager(manager); err != nil {
		return fmt.Errorf("create Agent controller: %w", err)
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("add health check: %w", err)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("add readiness check: %w", err)
	}
	ctrl.Log.WithName("setup").Info("starting Kubernetes manager", "namespace", *kubernetesNamespace)
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
