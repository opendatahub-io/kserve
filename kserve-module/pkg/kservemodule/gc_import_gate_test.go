package kservemodule

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGcCollectorImportGate is the merge-time static gate from RHAISTRAT-2173
// (AC #5). The platform's GC collector (odh-platform-utilities/pkg/controller/gc)
// deletes cluster-scoped resources whose stamped platform version differs from
// the current release — exactly the upgrade case Approach A depends on
// surviving: old-release CSRs must stay on the cluster so InferenceServices
// pinned to them keep serving.
//
// kserve-module does not import the collector today, so CSRs are retained by
// wiring, not by contract. This gate turns that wiring into a contract: if
// anyone ever imports the collector, ClusterServingRuntime must be registered
// as unremovable at the same time, or this test fails the build.
//
// If you legitimately need the collector: register ClusterServingRuntime via
// WithUnremovables, keep clusterServingRuntimeGK in unownedGroupKinds, and
// update the registration evidence below if the mechanism changes.
const gcCollectorPkg = "github.com/opendatahub-io/odh-platform-utilities/pkg/controller/gc"

func TestGcCollectorImportGate(t *testing.T) {
	// kserve-module root is two levels above this package's directory.
	const root = "../.."
	var importers []string
	var allSrc strings.Builder
	var constantsSrc string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "vendor" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		allSrc.Write(src)
		if filepath.Base(path) == "constants.go" {
			constantsSrc = string(src)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, parser.ImportsOnly)
		if err != nil {
			return nil // build-tagged or generated files — skip
		}
		for _, imp := range file.Imports {
			if strings.Trim(imp.Path.Value, `"`) == gcCollectorPkg {
				importers = append(importers, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking kserve-module source: %v", err)
	}

	if len(importers) == 0 {
		return // Gate passes: the collector is not wired in.
	}

	// The collector is imported — require the unremovable registration for CSRs.
	if !strings.Contains(allSrc.String(), "WithUnremovables") {
		t.Fatalf(
			"GC collector imported by:\n%s\n\n"+
				"ClusterServingRuntime must be registered as unremovable before the collector "+
				"can be wired in (RHAISTRAT-2173 AC #5): the collector deletes cluster-scoped "+
				"resources whose stamped version differs from the current release, which would "+
				"sweep old-release CSRs that running InferenceServices depend on. Register it "+
				"via WithUnremovables and keep clusterServingRuntimeGK in unownedGroupKinds.",
			strings.Join(importers, "\n"))
	}
	if !strings.Contains(constantsSrc, "clusterServingRuntimeGK") {
		t.Fatalf(
			"GC collector imported by:\n%s\n\n"+
				"but clusterServingRuntimeGK is no longer registered in constants.go. CSRs must "+
				"stay in unownedGroupKinds (and be registered unremovable with the collector) "+
				"per RHAISTRAT-2173 AC #5.",
			strings.Join(importers, "\n"))
	}
}
