package fixture

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule"
)

type TestEnv struct {
	Client     client.Client
	Deployer   *MockDeployer
	Reconciler *kservemodule.KserveModuleReconciler
}

func SetupTestEnv(ctx context.Context) *TestEnv {
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))
	gomega.SetDefaultEventuallyTimeout(30 * time.Second)
	gomega.SetDefaultEventuallyPollingInterval(250 * time.Millisecond)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(platformv1alpha1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(ProjectRoot(), "config", "crd")},
		ErrorIfCRDPathMissing: true,
		Scheme:                scheme,
	}

	cfg, err := env.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	cli, err := client.New(cfg, client.Options{Scheme: scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme,
		Metrics:        metricsserver.Options{BindAddress: "0"},
		LeaderElection: false,
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	CreateCRD(ctx, cli, "operators.coreos.com", "v1alpha1", "Subscription", apiextensionsv1.NamespaceScoped)

	workDir := ginkgo.GinkgoT().TempDir()
	WriteMinimalManifests(workDir)

	deployer := &MockDeployer{}
	reconciler := &kservemodule.KserveModuleReconciler{
		Client:                mgr.GetClient(),
		Scheme:                mgr.GetScheme(),
		ManifestsTemplatePath: workDir,
		Deployer:              deployer,
	}
	reconciler.SetWorkDir(workDir)
	reconciler.SetClusterType(cluster.ClusterTypeOpenShift)
	gomega.Expect(reconciler.SetupWithManager(mgr)).To(gomega.Succeed())

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "opendatahub"}}
	gomega.Expect(cli.Create(ctx, ns)).To(gomega.Succeed())

	mgrCtx, mgrCancel := context.WithCancel(ctx)
	go func() {
		defer ginkgo.GinkgoRecover()
		gomega.Expect(mgr.Start(mgrCtx)).To(gomega.Succeed())
	}()

	ginkgo.DeferCleanup(func() {
		mgrCancel()
		gomega.Expect(env.Stop()).To(gomega.Succeed())
	})

	return &TestEnv{
		Client:     cli,
		Deployer:   deployer,
		Reconciler: reconciler,
	}
}

// WriteMinimalManifests creates minimal kustomize trees for both OCP and XKS paths.
func WriteMinimalManifests(workDir string) {
	kserveManifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: inferenceservice-config
  namespace: opendatahub
data:
  ingress: "{}"
`
	modelCtrlManifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: odh-model-controller-config
  namespace: opendatahub
data:
  enabled: "true"
`
	writeKustomizeDir(filepath.Join(workDir, "kserve", "overlays", "odh"), kserveManifest)
	writeKustomizeDir(filepath.Join(workDir, "kserve", "overlays", "odh-xks"), kserveManifest)
	writeKustomizeDir(filepath.Join(workDir, "modelcontroller", "base"), modelCtrlManifest)
}

func writeKustomizeDir(dir, manifest string) {
	gomega.Expect(os.MkdirAll(dir, 0o755)).To(gomega.Succeed())

	kustomization := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- resource.yaml
`
	gomega.Expect(os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kustomization), 0o644)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(filepath.Join(dir, "resource.yaml"), []byte(manifest), 0o644)).To(gomega.Succeed())
}
