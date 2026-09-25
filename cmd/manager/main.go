// SPDX-FileCopyrightText: 2026 Ashon
// SPDX-License-Identifier: MIT

// Command manager runs the kgenesis infrastructure provider controllers.
//
// It is deployed into the bootstrap cluster by `kg init` and moves to the
// workload cluster with `kg eject`, alongside the Cluster API controllers.
package main

import (
	"flag"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infrav1 "github.com/Ashon/kg/api/v1alpha1"
	kgcontroller "github.com/Ashon/kg/internal/controller"
	"github.com/Ashon/kg/internal/version"
)

// managerName is what this binary calls itself in a version line. The CLI is
// kg; this is the controller it installs.
const managerName = "kgenesis-manager"

var scheme = runtime.NewScheme()

func init() {
	must(clientgoscheme.AddToScheme(scheme))
	must(clusterv1.AddToScheme(scheme))
	must(infrav1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr          string
		healthAddr           string
		enableLeaderElection bool
		concurrency          int
		watchNamespace       string
		showVersion          bool
	)

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "Address the metrics endpoint binds to.")
	flag.StringVar(&healthAddr, "health-probe-bind-address", ":9440", "Address the health probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true, "Run leader election so only one manager is active.")
	flag.IntVar(&concurrency, "concurrency", 5,
		"How many hosts may be reconciled at once. Each in-flight reconcile holds one SSH connection.")
	flag.StringVar(&watchNamespace, "namespace", "",
		"Restrict the manager to a single namespace. Empty watches all namespaces.")
	flag.BoolVar(&showVersion, "version", false, "Print the version and exit.")

	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	if showVersion {
		fmt.Println(version.String(managerName))
		return
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	setupLog := ctrl.Log.WithName("setup")

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: healthAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "kgenesis-infrastructure.infrastructure.kgenesis.io",
	}
	if watchNamespace != "" {
		options.Cache.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "Unable to start the manager")
		os.Exit(1)
	}

	ctrlOpts := ctrlcontroller.Options{MaxConcurrentReconciles: concurrency}

	if err := (&kgcontroller.HostReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr, ctrlOpts); err != nil {
		setupLog.Error(err, "Unable to set up a controller", "controller", "Host")
		os.Exit(1)
	}
	if err := (&kgcontroller.HostClusterReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr, ctrlOpts); err != nil {
		setupLog.Error(err, "Unable to set up a controller", "controller", "HostCluster")
		os.Exit(1)
	}
	if err := (&kgcontroller.HostMachineReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr, ctrlOpts); err != nil {
		setupLog.Error(err, "Unable to set up a controller", "controller", "HostMachine")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register the ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting the kgenesis infrastructure provider", "version", version.String(managerName))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Manager exited with an error")
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
