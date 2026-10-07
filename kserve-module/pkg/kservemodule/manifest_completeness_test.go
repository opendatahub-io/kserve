package kservemodule

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

// TestImageParamMapKeysMatchParamsEnv validates that every key in a component's
// imageMap has a corresponding entry in its checked-in params.env or fixture.
//
// A mismatch means the RELATED_IMAGE env var override for that key will be
// silently ignored by applyParams.
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

			// No params.env entry may be stale, and every imageMap key must be
			// present in the component's params.env or external fixture.
			for key := range params {
				g.Expect(comp.imageMap).Should(HaveKey(key),
					"params.env at %s has key %q with no matching imageMap entry — "+
						"the key will never be overridden via RELATED_IMAGE", paramsPath, key)
			}
			for key := range comp.imageMap {
				g.Expect(params).Should(HaveKey(key),
					"params.env at %s is missing imageMap key %q for component %q",
					paramsPath, key, comp.name)
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
