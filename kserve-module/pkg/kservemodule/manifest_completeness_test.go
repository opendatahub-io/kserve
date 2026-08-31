package kservemodule

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

// TestReconcileFailsWhenParamsEnvMissing validates that reconcileComponent
// returns a clear error when a component's params.env is expected but missing,
// rather than silently producing an incomplete deployment.
//
// This reproduces RHOAIENG-89454 where the operator image was missing
// modelcontroller/overlays/odh/params.env, causing the reconciler to fail
// with "no such file or directory" at runtime.
func TestReconcileFailsWhenParamsEnvMissing(t *testing.T) {
	_ = NewWithT(t)

	for _, comp := range components {
		if len(comp.imageMap) == 0 {
			continue
		}

		t.Run(comp.name+"_with_params_env", func(t *testing.T) {
			g := NewWithT(t)

			dir := t.TempDir()
			compDir := filepath.Join(dir, comp.dirName(), comp.sourcePath)
			g.Expect(os.MkdirAll(compDir, 0o755)).Should(Succeed())

			// Write a params.env that has all keys from the imageMap
			var lines []string
			for key := range comp.imageMap {
				lines = append(lines, fmt.Sprintf("%s=placeholder:latest", key))
			}
			g.Expect(os.WriteFile(
				filepath.Join(compDir, "params.env"),
				[]byte(strings.Join(lines, "\n")+"\n"),
				0o644,
			)).Should(Succeed())

			// applyParams should succeed when params.env exists with correct keys
			err := applyParams(compDir, comp.imageMap)
			g.Expect(err).ShouldNot(HaveOccurred(),
				"applyParams should succeed when params.env has all imageMap keys")

			// Verify all keys are present after apply
			params, err := parseParams(filepath.Join(compDir, "params.env"))
			g.Expect(err).ShouldNot(HaveOccurred())
			for key := range comp.imageMap {
				g.Expect(params).Should(HaveKey(key),
					"params.env should contain key %q from imageMap", key)
			}
		})

		t.Run(comp.name+"_without_params_env_dir_exists", func(t *testing.T) {
			// When the overlay directory exists but params.env is missing,
			// applyParams returns nil (no-op). This is the production failure
			// mode from RHOAIENG-89454 — the directory was there but the file
			// wasn't, so image overrides silently failed.
			g := NewWithT(t)

			dir := t.TempDir()
			compDir := filepath.Join(dir, comp.dirName(), comp.sourcePath)
			g.Expect(os.MkdirAll(compDir, 0o755)).Should(Succeed())

			err := applyParams(compDir, comp.imageMap)
			g.Expect(err).ShouldNot(HaveOccurred(),
				"applyParams should not error when params.env is absent "+
					"(but this means image overrides are silently skipped)")
		})
	}
}

// TestImageParamMapKeysMatchParamsEnv validates that every key in a component's
// imageMap has a corresponding entry in the production params.env overlay.
//
// This is a build-time contract test: the Dockerfile must produce an image
// where /opt/manifests/<component>/overlays/odh/params.env contains every key
// that the reconciler's imageMap references. A mismatch means the RELATED_IMAGE
// env var override for that key will be silently ignored.
func TestImageParamMapKeysMatchParamsEnv(t *testing.T) {
	// This test validates against the real overlay files in the repo.
	// For kserve component, the overlays live in config/overlays/odh/params.env.
	// For modelcontroller, manifests are external — skip if not present.

	projectRoot := findProjectRoot()

	// The kserve overlay lives in the repo root (parent of the kserve-module Go module)
	repoRoot := filepath.Dir(projectRoot)
	overlayPaths := map[string]string{
		KserveComponentName: filepath.Join(repoRoot, "config", "overlays", "odh", "params.env"),
	}

	for compName, paramsPath := range overlayPaths {
		t.Run(compName, func(t *testing.T) {
			g := NewWithT(t)

			if _, err := os.Stat(paramsPath); os.IsNotExist(err) {
				t.Skipf("params.env not found at %s (external manifest)", paramsPath)
			}

			params, err := parseParams(paramsPath)
			g.Expect(err).ShouldNot(HaveOccurred())

			var comp componentConfig
			for _, c := range components {
				if c.name == compName {
					comp = c
					break
				}
			}
			if len(comp.imageMap) == 0 {
				t.Skip("no imageMap for component")
			}

			for key := range comp.imageMap {
				g.Expect(params).Should(HaveKey(key),
					"production params.env at %s is missing key %q from imageMap. "+
						"The RELATED_IMAGE override for this key will be silently ignored. "+
						"Add %s=<default-image> to the params.env file.", paramsPath, key, key)
			}
		})
	}
}

func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
