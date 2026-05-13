package main

import (
	"errors"
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule"
)

const manifestsPath = "/opt/manifests"

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
		enableLeaderElect bool
		probeAddr         string
	)

	flag.BoolVar(&enableLeaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&probeAddr, "health-probe-addr", ":8081", "The address the probe endpoint binds to.")

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if fi, err := os.Stat(manifestsPath); err != nil {
		setupLog.Error(err, "manifests path is not accessible", "path", manifestsPath)
		os.Exit(1)
	} else if !fi.IsDir() {
		setupLog.Error(errors.New("not a directory"), "manifests path is not a directory", "path", manifestsPath)
		os.Exit(1)
	}

	leaderNS := os.Getenv("POD_NAMESPACE")
	if enableLeaderElect && leaderNS == "" {
		setupLog.Error(errors.New("POD_NAMESPACE not set"), "leader election requires POD_NAMESPACE")
		os.Exit(1)
	}

	mgrOpts := ctrl.Options{
		Scheme:                  scheme,
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElect,
		LeaderElectionID:        "kserve-module-controller-manager",
		LeaderElectionNamespace: leaderNS,
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), mgrOpts)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("setting up kserve-module controller")
	if err = (&kservemodule.KserveModuleReconciler{
		Client:        mgr.GetClient(),
		Scheme:        mgr.GetScheme(),
		ManifestsPath: manifestsPath,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "kserve-module")
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
