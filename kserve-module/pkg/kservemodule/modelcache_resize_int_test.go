package kservemodule_test

import (
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/api/common"
	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule"
	"github.com/opendatahub-io/kserve-module/pkg/kservemodule/fixture"
)

var _ = DescribeTable("ModelCache capacity", func(ctx SpecContext, target string) {
	cli := testEnv.Client
	workDir := GinkgoT().TempDir()
	fixture.WriteMinimalManifests(workDir)
	path := filepath.Join(workDir, kservemodule.KserveComponentName, kservemodule.ModelCacheManifestSourcePath)
	Expect(os.MkdirAll(path, 0o750)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(path, "kustomization.yaml"), []byte("resources:\n- agent.yaml\n"), 0o600)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(path, "agent.yaml"), []byte(`apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kserve-localmodelnode-agent
spec:
  selector:
    matchLabels:
      app: cache-agent
  template:
    metadata:
      labels:
        app: cache-agent
    spec:
      containers:
      - name: agent
        image: example.com/agent:test
      volumes:
      - name: models
        persistentVolumeClaim:
          claimName: kserve-localmodelnode-pvc
`), 0o600)).To(Succeed())
	saved := testEnv.Reconciler.WorkDir()
	testEnv.Reconciler.SetWorkDir(workDir)
	testEnv.Reconciler.Deployer = kservemodule.NewDeployer()
	DeferCleanup(func() { testEnv.Reconciler.SetWorkDir(saved) })
	fixture.CreateCRD(ctx, cli, "serving.kserve.io", "v1alpha1", "LocalModelNodeGroup", apiextensionsv1.ClusterScoped)
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "model-serving-cache", Labels: map[string]string{odhLabels.PlatformPartOf: kservemodule.KserveComponentName}}}
	Expect(client.IgnoreAlreadyExists(cli.Create(ctx, ns))).To(Succeed())
	Expect(cli.Get(ctx, client.ObjectKeyFromObject(ns), ns)).To(Succeed())
	ns.Annotations = map[string]string{"openshift.io/sa.scc.mcs": "s0:c1,c2"}
	Expect(cli.Update(ctx, ns)).To(Succeed())

	pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "kserve-localmodelnode-pv"}}
	pvc := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "kserve-localmodelnode-pvc", Namespace: ns.Name}}
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "kserve-localmodelnode-agent", Namespace: ns.Name}}
	group := &unstructured.Unstructured{}
	group.SetAPIVersion("serving.kserve.io/v1alpha1")
	group.SetKind("LocalModelNodeGroup")
	group.SetName("workers")
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}
	Expect(client.IgnoreAlreadyExists(cli.Create(ctx, node))).To(Succeed())
	cr := fixture.KserveCR()
	cr.Spec.ModelCache = &platformv1alpha1.ModelCacheSpec{ManagementState: common.Managed, CacheSize: quantity("100Gi"), NodeNames: []string{"worker"}}
	Expect(cli.Create(ctx, cr)).To(Succeed())
	DeferCleanup(func(ctx SpecContext) {
		deleteAndWaitGone(ctx, cr)
		for _, obj := range []client.Object{ds, pvc, pv, group} {
			if err := cli.Get(ctx, client.ObjectKeyFromObject(obj), obj); k8serr.IsNotFound(err) {
				continue
			} else {
				Expect(err).NotTo(HaveOccurred())
			}
			obj.SetFinalizers(nil) // Simulate controllers absent from envtest, including GC.
			Expect(cli.Update(ctx, obj)).To(Succeed())
			Expect(client.IgnoreNotFound(cli.Delete(ctx, obj))).To(Succeed())
		}
	})
	Eventually(func(g Gomega) {
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)).To(Succeed())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(pv), pv)).To(Succeed())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(Succeed())
	}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
	pvc.Status.Phase = corev1.ClaimBound
	pvc.Status.Capacity = pvc.Spec.Resources.Requests.DeepCopy()
	Expect(cli.Status().Update(ctx, pvc)).To(Succeed())
	pv.Spec.ClaimRef = &corev1.ObjectReference{Name: pvc.Name, Namespace: pvc.Namespace, UID: pvc.UID}
	Expect(cli.Update(ctx, pv)).To(Succeed())
	oldPVC, oldPV := pvc.UID, pv.UID
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "cache-consumer", Namespace: ns.Name}, Spec: ds.Spec.Template.Spec}
	Expect(cli.Create(ctx, pod)).To(Succeed())
	DeferCleanup(func(ctx SpecContext) {
		Expect(client.IgnoreNotFound(cli.Delete(ctx, pod, client.GracePeriodSeconds(0)))).To(Succeed())
	})
	// Hold deletion as real workload/protection controllers would until we release it.
	for _, obj := range []client.Object{ds, pvc, pv} {
		obj.SetFinalizers([]string{"test.kserve.io/hold"})
		Expect(cli.Update(ctx, obj)).To(Succeed())
	}
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		if err := cli.Get(ctx, client.ObjectKeyFromObject(cr), cr); err != nil {
			return err
		}
		cr.Spec.ModelCache.CacheSize = quantity(target)
		return cli.Update(ctx, cr)
	})).To(Succeed())
	for i, obj := range []client.Object{ds, pvc, pv} {
		Eventually(func(g Gomega) {
			g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(cr), cr)).To(Succeed())
			g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
			g.Expect(obj.GetDeletionTimestamp().IsZero()).To(BeFalse(), "%v", cr.Status.Conditions)
		}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
		if i < 2 {
			next := []client.Object{pvc, pv}[i]
			Consistently(func(g Gomega) {
				g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(next), next)).To(Succeed())
				g.Expect(next.GetDeletionTimestamp().IsZero()).To(BeTrue())
				if i == 0 {
					g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(Succeed())
					g.Expect(ds.GetDeletionTimestamp().IsZero()).To(BeFalse())
				} else {
					g.Expect(k8serr.IsNotFound(cli.Get(ctx, client.ObjectKeyFromObject(ds), ds))).To(BeTrue())
				}
			}).WithContext(ctx).WithTimeout(500 * time.Millisecond).Should(Succeed())
		}
		Expect(cli.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		obj.SetFinalizers(nil)
		Expect(cli.Update(ctx, obj)).To(Succeed())
		if i == 0 {
			Consistently(func(g Gomega) {
				g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)).To(Succeed())
				g.Expect(pvc.DeletionTimestamp.IsZero()).To(BeTrue())
			}).WithContext(ctx).WithTimeout(500 * time.Millisecond).Should(Succeed())
			Expect(cli.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed())
		}
	}
	Eventually(func(g Gomega) {
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)).To(Succeed())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(pv), pv)).To(Succeed())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(ds), ds)).To(Succeed())
		g.Expect(pvc.UID).NotTo(Equal(oldPVC))
		g.Expect(pv.UID).NotTo(Equal(oldPV))
		g.Expect(pvc.Spec.Resources.Requests.Storage().Cmp(*quantity(target))).To(BeZero())
		g.Expect(pv.Spec.Capacity.Storage().Cmp(*quantity(target))).To(BeZero())
		g.Expect(ds.DeletionTimestamp.IsZero()).To(BeTrue())
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(group), group)).To(Succeed())
		limit, _, err := unstructured.NestedString(group.Object, "spec", "storageLimit")
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(limit).To(Equal(target))
		for _, fields := range [][]string{{"persistentVolumeSpec", "capacity", "storage"}, {"persistentVolumeClaimSpec", "resources", "requests", "storage"}} {
			value, _, err := unstructured.NestedString(group.Object, append([]string{"spec"}, fields...)...)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(value).To(Equal(target))
		}
	}).WithContext(ctx).WithTimeout(30 * time.Second).Should(Succeed())
}, Entry("growth", "200Gi"), Entry("shrink", "50Gi"))

func quantity(value string) *resource.Quantity {
	q := resource.MustParse(value)
	return &q
}
