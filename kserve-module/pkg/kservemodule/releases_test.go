package kservemodule

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

func TestLoadComponentReleases_ParsesBothFiles(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	writeMetadata(t, dir, "kserve", `releases:
  - name: ComponentA
    version: v0.0.1
    repoUrl: https://example.com/a
  - name: ComponentB
    version: v0.0.2
    repoUrl: https://example.com/b
`)
	writeMetadata(t, dir, "odh-model-controller", `releases:
  - name: ComponentA
    version: v0.0.3
    repoUrl: https://example.com/a
  - name: ComponentC
    version: v0.0.4
    repoUrl: https://example.com/c
`)

	releases, err := loadComponentReleases(dir, []string{"kserve", "odh-model-controller"})
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(releases).Should(HaveLen(3))
	g.Expect(releases[0].Name).Should(Equal("ComponentA"))
	g.Expect(releases[0].Version).Should(Equal("v0.0.1"))
	g.Expect(releases[1].Name).Should(Equal("ComponentB"))
	g.Expect(releases[2].Name).Should(Equal("ComponentC"))
}

func TestLoadComponentReleases_MissingFile(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()
	writeMetadata(t, dir, "kserve", `releases:
  - name: ComponentA
    version: v0.0.1
    repoUrl: https://example.com/a
`)

	releases, err := loadComponentReleases(dir, []string{"kserve", "nonexistent"})
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(releases).Should(HaveLen(1))
}

func TestLoadComponentReleases_Fallback(t *testing.T) {
	g := NewWithT(t)

	dir := t.TempDir()

	releases, err := loadComponentReleases(dir, []string{"kserve", "odh-model-controller"})
	g.Expect(err).ShouldNot(HaveOccurred())
	g.Expect(releases).Should(HaveLen(1))
	g.Expect(releases[0].Name).Should(Equal("KServe"))
	g.Expect(releases[0].Version).Should(Equal("unknown"))
	g.Expect(releases[0].RepoURL).Should(Equal("https://github.com/kserve/kserve/"))
}

func TestSetReleaseStatus_ExcludesModelControllerOnXKS(t *testing.T) {
	kserveMeta := `releases:
  - name: KServe
    version: v0.19.0
    repoUrl: https://github.com/kserve/kserve/
`
	omcMeta := `releases:
  - name: OpenVINO Model Server
    version: v2025.4
    repoUrl: https://github.com/openvinotoolkit/model_server
  - name: MLServer
    version: 1.7.1
    repoUrl: https://github.com/SeldonIO/MLServer
`

	omcRuntimes := []string{"OpenVINO Model Server", "MLServer"}

	tests := []struct {
		name            string
		clusterType     cluster.ClusterType
		wantOMCRuntimes bool
	}{
		{
			name:            "OCP includes modelcontroller runtimes",
			clusterType:     cluster.ClusterTypeOpenShift,
			wantOMCRuntimes: true,
		},
		{
			name:            "XKS excludes modelcontroller runtimes",
			clusterType:     cluster.ClusterTypeKubernetes,
			wantOMCRuntimes: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			dir := t.TempDir()
			writeMetadata(t, dir, KserveComponentName, kserveMeta)
			writeMetadata(t, dir, OdhModelControllerComponentName, omcMeta)

			r := newReconcilerWithFakeClient()
			r.ManifestsTemplatePath = dir
			r.SetClusterType(tc.clusterType)

			kserve := &platformv1alpha1.Kserve{}
			r.setReleaseStatus(context.Background(), kserve)

			releases := kserve.GetReleaseStatus().Releases
			// KServe (from the kserve component) is always present on both platforms.
			g.Expect(releases).To(ContainElement(HaveField("Name", "KServe")))

			for _, rt := range omcRuntimes {
				if tc.wantOMCRuntimes {
					g.Expect(releases).To(ContainElement(HaveField("Name", rt)))
				} else {
					g.Expect(releases).NotTo(ContainElement(HaveField("Name", rt)))
				}
			}
		})
	}
}

func writeMetadata(t *testing.T, base, component, content string) {
	t.Helper()
	dir := filepath.Join(base, component)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "component_metadata.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
