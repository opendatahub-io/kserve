package kservemodule

import (
	"os"
	"testing"

	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

// localModelSCCManifestPath points at the source-of-truth SecurityContextConstraints
// manifest consumed both by the legacy operator kustomize overlay and by
// ModelCacheManifestSourcePath ("overlays/odh-modelcache") here in kserve-module.
const localModelSCCManifestPath = "../../../config/overlays/odh-modelcache/localmodel-scc.yaml"

type sccStrategy struct {
	Type string `yaml:"type"`
}

type securityContextConstraintsManifest struct {
	Kind           string      `yaml:"kind"`
	SELinuxContext sccStrategy `yaml:"seLinuxContext"`
}

// TestLocalModelSCC_SELinuxContextMustRunAs guards against regressing the
// openshift-ai-localmodel-scc SCC back to seLinuxContext.type: RunAsAny.
//
// RunAsAny lets CRI-O assign the kserve-localmodelnode-agent DaemonSet pods an
// arbitrary, per-pod SELinux MCS category instead of deriving it from the
// namespace's `openshift.io/sa.scc.mcs` annotation. The model-cache
// permission-fix Job relabels the shared hostPath directory to that
// namespace-derived category, so a RunAsAny agent pod permanently mismatches
// it and every model download gets stuck in NodeDownloadPending with
// permission-denied errors. MustRunAs (with no explicit level) makes
// OpenShift derive the same namespace category for the agent too.
func TestLocalModelSCC_SELinuxContextMustRunAs(t *testing.T) {
	g := NewWithT(t)

	raw, err := os.ReadFile(localModelSCCManifestPath)
	g.Expect(err).NotTo(HaveOccurred(), "failed to read %s", localModelSCCManifestPath)

	var scc securityContextConstraintsManifest
	g.Expect(yaml.Unmarshal(raw, &scc)).To(Succeed())

	g.Expect(scc.Kind).To(Equal("SecurityContextConstraints"))
	g.Expect(scc.SELinuxContext.Type).To(Equal("MustRunAs"),
		"openshift-ai-localmodel-scc must use seLinuxContext.type: MustRunAs so the "+
			"kserve-localmodelnode-agent DaemonSet gets the namespace's SELinux MCS "+
			"category instead of an arbitrary one from CRI-O")
}
