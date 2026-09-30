package kservemodule

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestParseParams_BasicKeyValue(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	paramsFile := filepath.Join(dir, "params.env")
	g.Expect(os.WriteFile(paramsFile, []byte("key1=val1\nkey2=val2\n# comment\n\nkey3=val3\n"), 0o644)).ShouldNot(HaveOccurred())

	params, err := parseParams(paramsFile)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params).Should(HaveLen(3))
	g.Expect(params["key1"]).Should(Equal("val1"))
	g.Expect(params["key2"]).Should(Equal("val2"))
	g.Expect(params["key3"]).Should(Equal("val3"))
}

func TestParseParams_SkipsComments(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	paramsFile := filepath.Join(dir, "params.env")
	g.Expect(os.WriteFile(paramsFile, []byte("# this is a comment\nkey=val\n  # indented comment\n"), 0o644)).ShouldNot(HaveOccurred())

	params, err := parseParams(paramsFile)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params).Should(HaveLen(1))
	g.Expect(params["key"]).Should(Equal("val"))
}

func TestApplyParams_OverridesFromEnv(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(dir, "params.env"), []byte("my-image=old-value\n"), 0o644)).ShouldNot(HaveOccurred())

	imageMap := map[string]string{"my-image": "TEST_RELATED_IMAGE"}
	t.Setenv("TEST_RELATED_IMAGE", "new-value")

	err := applyParams(dir, imageMap)
	g.Expect(err).ShouldNot(HaveOccurred())

	params, err := parseParams(filepath.Join(dir, "params.env"))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params["my-image"]).Should(Equal("new-value"))
}

func TestApplyParams_PreservesWhenEnvNotSet(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(dir, "params.env"), []byte("my-image=original\n"), 0o644)).ShouldNot(HaveOccurred())

	imageMap := map[string]string{"my-image": "UNSET_ENV_VAR"}

	err := applyParams(dir, imageMap)
	g.Expect(err).ShouldNot(HaveOccurred())

	params, err := parseParams(filepath.Join(dir, "params.env"))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params["my-image"]).Should(Equal("original"))
}

func TestApplyParams_PreservesWhenEnvEmpty(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(dir, "params.env"), []byte("my-image=original\n"), 0o644)).ShouldNot(HaveOccurred())

	imageMap := map[string]string{"my-image": "EMPTY_ENV_VAR"}
	t.Setenv("EMPTY_ENV_VAR", "")

	err := applyParams(dir, imageMap)
	g.Expect(err).ShouldNot(HaveOccurred())

	params, err := parseParams(filepath.Join(dir, "params.env"))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params["my-image"]).Should(Equal("original"))
}

func TestApplyParams_FileNotExist(t *testing.T) {
	g := NewWithT(t)

	err := applyParams(t.TempDir(), nil)
	g.Expect(err).ShouldNot(HaveOccurred())
}

func TestResolveParamsEnv_FallsBackToBase(t *testing.T) {
	g := NewWithT(t)

	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	overlayDir := filepath.Join(root, "overlays", "odh")
	g.Expect(os.MkdirAll(baseDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.MkdirAll(overlayDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(baseDir, "params.env"), []byte("img=val\n"), 0o644)).ShouldNot(HaveOccurred())

	resolved, err := resolveParamsEnv(overlayDir)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resolved).Should(Equal(filepath.Join(baseDir, "params.env")))
}

func TestResolveParamsEnv_PrefersOverlay(t *testing.T) {
	g := NewWithT(t)

	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	overlayDir := filepath.Join(root, "overlays", "odh")
	g.Expect(os.MkdirAll(baseDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.MkdirAll(overlayDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(baseDir, "params.env"), []byte("img=base\n"), 0o644)).ShouldNot(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(overlayDir, "params.env"), []byte("img=overlay\n"), 0o644)).ShouldNot(HaveOccurred())

	resolved, err := resolveParamsEnv(overlayDir)
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(resolved).Should(Equal(filepath.Join(overlayDir, "params.env")))
}

func TestApplyParams_FallsBackToBase(t *testing.T) {
	g := NewWithT(t)

	root := t.TempDir()
	baseDir := filepath.Join(root, "base")
	overlayDir := filepath.Join(root, "overlays", "odh")
	g.Expect(os.MkdirAll(baseDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.MkdirAll(overlayDir, 0o755)).ShouldNot(HaveOccurred())
	g.Expect(os.WriteFile(filepath.Join(baseDir, "params.env"), []byte("my-image=old-value\n"), 0o644)).ShouldNot(HaveOccurred())

	imageMap := map[string]string{"my-image": "TEST_FALLBACK_IMAGE"}
	t.Setenv("TEST_FALLBACK_IMAGE", "new-value")

	err := applyParams(overlayDir, imageMap)
	g.Expect(err).ShouldNot(HaveOccurred())

	params, err := parseParams(filepath.Join(baseDir, "params.env"))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params["my-image"]).Should(Equal("new-value"))
}

func TestApplyParams_ExtraParamsMap(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(dir, "params.env"), []byte("NAMESPACE=default\n"), 0o644)).ShouldNot(HaveOccurred())

	extra := map[string]string{"NAMESPACE": "opendatahub"}

	err := applyParams(dir, nil, extra)
	g.Expect(err).ShouldNot(HaveOccurred())

	params, err := parseParams(filepath.Join(dir, "params.env"))
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(params["NAMESPACE"]).Should(Equal("opendatahub"))
}

func TestBuildCertManagerParams_Defaults(t *testing.T) {
	g := NewWithT(t)

	params := buildCertManagerParams("test-ns", nil, defaultCertManagerNS)
	g.Expect(params["NAMESPACE"]).Should(Equal("test-ns"))
	g.Expect(params["ISSUER_REF_NAME"]).Should(Equal(defaultCAIssuerName))
	g.Expect(params["ISSUER_REF_KIND"]).Should(Equal(defaultIssuerRefKind))
	g.Expect(params["ISSUER_REF_GROUP"]).Should(Equal("cert-manager.io"))
	g.Expect(params["CA_SECRET_NAME"]).Should(Equal(defaultCertName))
	g.Expect(params["CA_SECRET_NAMESPACE"]).Should(Equal(defaultCertManagerNS))
	g.Expect(params["ISTIO_CA_CERTIFICATE_PATH"]).Should(Equal(defaultIstioCACertPath))
}

func TestBuildCertManagerParams_DynamicNamespace(t *testing.T) {
	g := NewWithT(t)

	params := buildCertManagerParams("test-ns", nil, "cert-manager-operator")
	g.Expect(params["CA_SECRET_NAMESPACE"]).Should(Equal("cert-manager-operator"))
}

func TestBuildCertManagerParams_ConfigMapValues(t *testing.T) {
	g := NewWithT(t)

	configData := map[string]string{
		certManagerIssuerRefNameKey:   "rhai-ca-issuer",
		certManagerIssuerRefKindKey:   "Issuer",
		certManagerCASecretNameKey:    "rhai-ca",
		certManagerIstioCACertPathKey: "/var/run/secrets/rhai/ca.crt",
	}

	params := buildCertManagerParams("test-ns", configData, defaultCertManagerNS)
	g.Expect(params["ISSUER_REF_NAME"]).Should(Equal("rhai-ca-issuer"))
	g.Expect(params["ISSUER_REF_KIND"]).Should(Equal("Issuer"))
	g.Expect(params["CA_SECRET_NAME"]).Should(Equal("rhai-ca"))
	g.Expect(params["ISTIO_CA_CERTIFICATE_PATH"]).Should(Equal("/var/run/secrets/rhai/ca.crt"))
	g.Expect(params["NAMESPACE"]).Should(Equal("test-ns"))
	g.Expect(params["ISSUER_REF_GROUP"]).Should(Equal("cert-manager.io"))
}

func TestBuildCertManagerParams_PartialConfigMap(t *testing.T) {
	g := NewWithT(t)

	configData := map[string]string{
		certManagerIssuerRefNameKey: "rhai-ca-issuer",
	}

	params := buildCertManagerParams("ns", configData, defaultCertManagerNS)
	g.Expect(params["ISSUER_REF_NAME"]).Should(Equal("rhai-ca-issuer"))
	g.Expect(params["ISSUER_REF_KIND"]).Should(Equal(defaultIssuerRefKind))
	g.Expect(params["CA_SECRET_NAME"]).Should(Equal(defaultCertName))
}

func componentByName(t *testing.T, name string) componentConfig {
	t.Helper()
	for _, comp := range components {
		if comp.name == name {
			return comp
		}
	}
	t.Fatalf("no component %q", name)
	return componentConfig{}
}

func xksParamsReconciler(namespace string) *KserveModuleReconciler {
	r := &KserveModuleReconciler{
		Client:                fake.NewClientBuilder().WithScheme(testScheme()).Build(),
		applicationsNamespace: namespace,
	}
	r.SetClusterType(cluster.ClusterTypeKubernetes)
	return r
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyComponentParams_XKSLeavesModelExpressBaseParamsAlone(t *testing.T) {
	g := NewWithT(t)
	comp := componentByName(t, ModelExpressComponentName)
	manifestDir := t.TempDir()
	bundle := filepath.Join(manifestDir, comp.dirName())
	g.Expect(os.MkdirAll(filepath.Join(bundle, comp.sourcePath), 0o750)).To(Succeed())
	g.Expect(os.MkdirAll(filepath.Join(bundle, comp.sourcePathXKS), 0o750)).To(Succeed())
	baseParams := filepath.Join(bundle, "base", "params.env")
	writeFile(t, baseParams,
		"MODELEXPRESS_OPERATOR_IMAGE=quay.io/opendatahub/odh-modelexpress-operator:odh-stable\n"+
			"MODELEXPRESS_SERVER_IMAGE=quay.io/opendatahub/odh-modelexpress:odh-stable\n")

	r := xksParamsReconciler("platform-ns")
	g.Expect(r.applyComponentParams(context.Background(), &platformv1alpha1.Kserve{},
		manifestDir, comp, comp.sourcePathXKS)).To(Succeed())

	params, err := parseParams(baseParams)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(params).To(HaveLen(2))
	g.Expect(params).To(HaveKey("MODELEXPRESS_OPERATOR_IMAGE"))
	g.Expect(params).To(HaveKey("MODELEXPRESS_SERVER_IMAGE"))
}

func TestApplyComponentParams_XKSWritesCertManagerParamsForOptedInComponents(t *testing.T) {
	for _, name := range []string{KserveComponentName, OdhModelControllerComponentName} {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			comp := componentByName(t, name)
			manifestDir := t.TempDir()
			bundle := filepath.Join(manifestDir, comp.dirName())
			g.Expect(os.MkdirAll(filepath.Join(bundle, comp.sourcePath), 0o750)).To(Succeed())
			xksParams := filepath.Join(bundle, comp.sourcePathXKS, "params.env")
			writeFile(t, xksParams, "NAMESPACE=opendatahub\nISSUER_REF_NAME=opendatahub-ca-issuer\n")

			r := xksParamsReconciler("platform-ns")
			g.Expect(r.applyComponentParams(context.Background(), &platformv1alpha1.Kserve{},
				manifestDir, comp, comp.sourcePathXKS)).To(Succeed())

			params, err := parseParams(xksParams)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(params).To(HaveKeyWithValue("NAMESPACE", "platform-ns"))
			g.Expect(params).To(HaveKeyWithValue("ISSUER_REF_NAME", defaultCAIssuerName))
		})
	}
}

func TestComponentsConfig_CertManagerParamsOnlyForKserveAndModelController(t *testing.T) {
	g := NewWithT(t)
	for _, comp := range components {
		want := comp.name == KserveComponentName || comp.name == OdhModelControllerComponentName
		g.Expect(comp.certManagerParams).To(Equal(want), "component %q", comp.name)
	}
}
