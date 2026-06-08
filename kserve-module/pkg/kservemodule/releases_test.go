package kservemodule

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
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
