package kservemodule

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

type componentMetadata struct {
	Releases []common.ComponentRelease `yaml:"releases"`
}

var fallbackReleases = []common.ComponentRelease{
	{Name: "KServe", Version: "unknown", RepoURL: "https://github.com/kserve/kserve/"},
}

func loadComponentReleases(manifestsPath string, componentDirs []string) ([]common.ComponentRelease, error) {
	seen := make(map[string]bool)
	var all []common.ComponentRelease

	for _, dir := range componentDirs {
		path := filepath.Join(manifestsPath, dir, "component_metadata.yaml")

		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}

		var meta componentMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", path, err)
		}

		for _, r := range meta.Releases {
			if seen[r.Name] {
				continue
			}
			seen[r.Name] = true
			all = append(all, r)
		}
	}

	if len(all) == 0 {
		return fallbackReleases, nil
	}

	return all, nil
}
