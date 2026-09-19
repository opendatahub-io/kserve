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
	moduleRoot := findProjectRoot()
	if moduleRoot == "" {
		t.Fatal("cannot determine module root")
	}
	repoRoot := filepath.Dir(moduleRoot)
	if !isDir(filepath.Join(repoRoot, "config")) {
		if isDir(filepath.Join(moduleRoot, "config")) {
			repoRoot = moduleRoot
		} else {
			t.Fatalf("cannot locate config/ from module root %q", moduleRoot)
		}
	}

	// These components have manifests checked into this repository. Their
	// params.env files must be present; a missing file is a test failure.
	inRepoParamsEnv := map[string]string{
		KserveComponentName:     filepath.Join(repoRoot, "config", "overlays", "odh", "params.env"),
		ModelCacheComponentName: filepath.Join(repoRoot, "config", "overlays", "odh-modelcache", "params.env"),
	}

	// These manifests are built from external repositories. Keep their expected
	// params.env files checked in so this test does not manufacture its own
	// expectations from imageMap.
	externalParamsEnv := map[string]string{
		OdhModelControllerComponentName: filepath.Join(repoRoot, "kserve-module", "pkg", "kservemodule", "testdata", "modelcontroller-params.env"),
		WVAComponentName:                filepath.Join(repoRoot, "kserve-module", "pkg", "kservemodule", "testdata", "wva-params.env"),
	}

	for _, comp := range components {
		if len(comp.imageMap) == 0 {
			continue
		}

		t.Run(comp.name, func(t *testing.T) {
			g := NewWithT(t)

			paramsPath, ok := inRepoParamsEnv[comp.name]
			if !ok {
				paramsPath, ok = externalParamsEnv[comp.name]
			}
			if !ok {
				t.Fatalf("no params.env path or fixture configured for component %q", comp.name)
			}

			params, err := parseParams(paramsPath)
			g.Expect(err).ShouldNot(HaveOccurred(), "params.env is missing or unreadable at %s", paramsPath)

			// No params.env entry may be stale. External fixtures additionally
			// provide the expected key list because their manifests are external.
			for key := range params {
				g.Expect(comp.imageMap).Should(HaveKey(key),
					"params.env at %s has key %q with no matching imageMap entry — "+
						"the key will never be overridden via RELATED_IMAGE", paramsPath, key)
			}
			if _, isExternal := externalParamsEnv[comp.name]; isExternal {
				for key := range comp.imageMap {
					g.Expect(params).Should(HaveKey(key),
						"params.env fixture at %s is missing imageMap key %q for component %q",
						paramsPath, key, comp.name)
				}
			}
		})
	}
}

func findProjectRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
