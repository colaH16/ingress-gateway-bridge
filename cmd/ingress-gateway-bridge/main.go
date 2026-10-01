// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/colaH16/ingress-gateway-bridge/internal/config"
	"github.com/colaH16/ingress-gateway-bridge/internal/controller"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func main() {
	var configPath, metricsAddress, probeAddress string
	var dryRun, leaderElection bool
	flag.StringVar(&configPath, "config", "bridge.yaml", "Path to class-to-Gateway bindings (restart after changes)")
	flag.BoolVar(&dryRun, "dry-run", true, "Log the HTTPRoute plan without API writes")
	flag.BoolVar(&leaderElection, "leader-elect", false, "Use a leader-election Lease; required when running multiple writer replicas")
	flag.StringVar(&metricsAddress, "metrics-bind-address", "0", "Metrics listen address; 0 disables metrics")
	flag.StringVar(&probeAddress, "health-probe-bind-address", "127.0.0.1:8081", "Health probe listen address")
	zapOptions := zap.Options{}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	if err := run(configPath, metricsAddress, probeAddress, dryRun, leaderElection); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath, metricsAddress, probeAddress string, dryRun, leaderElection bool) error {
	if dryRun && leaderElection {
		return fmt.Errorf("dry-run cannot use leader election because a Lease writes to the API")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, networkingv1.AddToScheme, gatewayv1.Install} {
		if err := add(scheme); err != nil {
			return err
		}
	}
	restConfig, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	manager, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddress},
		HealthProbeBindAddress: probeAddress,
		LeaderElection:         leaderElection,
		LeaderElectionID:       "ingress-gateway-bridge.colah16.github.io",
	})
	if err != nil {
		return err
	}
	reconciler := &controller.IngressReconciler{Client: manager.GetClient(), Config: cfg, DryRun: dryRun}
	if !dryRun {
		reconciler.Recorder = manager.GetEventRecorderFor("ingress-gateway-bridge")
	}
	if err := reconciler.SetupWithManager(context.Background(), manager); err != nil {
		return err
	}
	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := manager.AddReadyzCheck("readyz", func(request *http.Request) error {
		ctx, cancel := context.WithTimeout(request.Context(), time.Second)
		defer cancel()
		if !manager.GetCache().WaitForCacheSync(ctx) {
			return fmt.Errorf("cache is not synced")
		}
		return nil
	}); err != nil {
		return err
	}
	ctrl.Log.Info("starting Ingress bridge", "dryRun", dryRun, "classes", len(cfg.Classes), "controllerName", cfg.ControllerName)
	return manager.Start(ctrl.SetupSignalHandler())
}
