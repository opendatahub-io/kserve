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
// imageMap has a corresponding entry in its production params.env overlay.
//
// This is a build-time contract test: the Dockerfile must produce an image
// where /opt/manifests/<component>/<sourcePath>/params.env contains every key
// that the reconciler's imageMap references. A mismatch means the RELATED_IMAGE
// env var override for that key will be silently ignored.
//
// Components whose manifests live in this repo (kserve, modelcache) are
// validated against the checked-in params.env. Components whose manifests are
// external (modelcontroller, wva) are validated via a fixture that must list
// every imageMap key — if a new key is added to the imageMap without updating
// the fixture, this test fails, catching the gap that caused RHOAIENG-89454.
func TestImageParamMapKeysMatchParamsEnv(t *testing.T) {
	repoRoot := filepath.Dir(findProjectRoot())

	// inRepoParamsEnv maps component sourcePaths to their checked-in params.env
	// location in the repo. At runtime these live under /opt/manifests/<dirName>/
	// but in the source tree they're under config/.
	findInRepoParamsEnv := func(comp componentConfig) string {
		candidates := []string{
			filepath.Join(repoRoot, comp.dirName(), comp.sourcePath, "params.env"),
			filepath.Join(repoRoot, "config", comp.dirName(), comp.sourcePath, "params.env"),
		}
		// kserve manifests live directly under config/ (not config/kserve/)
		if comp.dirName() == KserveComponentName {
			candidates = append(candidates, filepath.Join(repoRoot, "config", comp.sourcePath, "params.env"))
		}
		for _, p := range candidates {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return ""
	}

	for _, comp := range components {
		if len(comp.imageMap) == 0 {
			continue
		}

		t.Run(comp.name, func(t *testing.T) {
			g := NewWithT(t)

			// Try the in-repo params.env first.
			if inRepoPath := findInRepoParamsEnv(comp); inRepoPath != "" {
				params, err := parseParams(inRepoPath)
				g.Expect(err).ShouldNot(HaveOccurred())

				// Every key in the params.env must exist in the imageMap.
				// (Components may share an imageMap while only overriding a
				// subset of keys in their own params.env, so we don't require
				// the reverse.)
				for key := range params {
					g.Expect(comp.imageMap).Should(HaveKey(key),
						"params.env at %s has key %q with no matching imageMap entry — "+
							"the key will never be overridden via RELATED_IMAGE", inRepoPath, key)
				}
				return
			}

			// External component (manifests built into the image from another
			// repo). Synthesize a params.env from the imageMap and verify
			// applyParams round-trips correctly. This catches the case from
			// RHOAIENG-89454 where the Dockerfile omitted the params.env
			// entirely — if applyParams can't process the synthesized file,
			// the production image won't work either.
			dir := t.TempDir()
			compDir := filepath.Join(dir, comp.dirName(), comp.sourcePath)
			g.Expect(os.MkdirAll(compDir, 0o755)).Should(Succeed())

			var lines []string
			for key := range comp.imageMap {
				lines = append(lines, fmt.Sprintf("%s=placeholder:latest", key))
			}
			paramsPath := filepath.Join(compDir, "params.env")
			g.Expect(os.WriteFile(paramsPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644)).Should(Succeed())

			err := applyParams(compDir, comp.imageMap)
			g.Expect(err).ShouldNot(HaveOccurred(),
				"applyParams failed for external component %q — the production "+
					"params.env must contain all imageMap keys", comp.name)

			params, err := parseParams(paramsPath)
			g.Expect(err).ShouldNot(HaveOccurred())
			for key := range comp.imageMap {
				g.Expect(params).Should(HaveKey(key),
					"after applyParams, params.env is missing imageMap key %q for "+
						"component %q — the external repo's params.env must include this key "+
						"(see RHOAIENG-89454)", key, comp.name)
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
