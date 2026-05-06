/*
Copyright 2023 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"crypto/tls"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	platformv1alpha1 "github.com/kserve/kserve/pkg/apis/platform/v1alpha1"
	"github.com/kserve/kserve/pkg/controller/kservemodule"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr       string
		enableLeaderElect bool
		probeAddr         string
		metricsSecure     bool
		enableHTTP2       bool
		manifestsPath     string
	)

	flag.StringVar(&metricsAddr, "metrics-addr", ":8443", "The address the metric endpoint binds to.")
	flag.BoolVar(&enableLeaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&probeAddr, "health-probe-addr", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&metricsSecure, "metrics-secure", true, "Whether to serve metrics via HTTPS.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "If set, HTTP/2 will be enabled for the metrics server.")
	flag.StringVar(&manifestsPath, "manifests-path", "/opt/manifests", "Base path for KServe manifests.")

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if fi, err := os.Stat(manifestsPath); err != nil {
		setupLog.Error(err, "manifests path is not accessible", "path", manifestsPath)
		os.Exit(1)
	} else if !fi.IsDir() {
		setupLog.Error(nil, "manifests path is not a directory", "path", manifestsPath)
		os.Exit(1)
	}

	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("disabling http/2")
		c.NextProtos = []string{"http/1.1"}
	}

	var tlsOpts []func(*tls.Config)
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: metricsSecure,
		TLSOpts:       tlsOpts,
	}
	if metricsSecure {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgrOpts := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElect,
		LeaderElectionID:       "kserve-module-controller-manager",
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("setting up KserveModule controller")
	if err = (&kservemodule.KserveModuleReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ManifestsPath: manifestsPath,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KserveModule")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "unable to run the manager")
		os.Exit(1)
	}
}
