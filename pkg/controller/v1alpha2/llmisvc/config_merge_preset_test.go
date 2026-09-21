/*
Copyright 2026 The KServe Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package llmisvc_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/controller/v1alpha2/llmisvc"
	kservetesting "github.com/kserve/kserve/pkg/testing"
)

// presetSources are both copies of the shipped presets: config/ is the source,
// charts/ is what Helm users install, and only a make target keeps them in step.
func presetSources(t *testing.T) []string {
	t.Helper()
	root := kservetesting.ProjectRoot()
	return []string{
		filepath.Join(root, "config", "llmisvcconfig"),
		filepath.Join(root, "charts", "kserve-runtime-configs", "files", "llmisvcconfigs"),
	}
}

type presetDoc struct {
	name string
	body string
}

// shippedPresetDocs returns every preset document from both copies. The charts
// file holds all presets in one multi-document YAML, which is why it needs
// splitting rather than a directory walk.
func shippedPresetDocs(t *testing.T) []presetDoc {
	t.Helper()
	var docs []presetDoc
	for _, dir := range presetSources(t) {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" || entry.Name() == "kustomization.yaml" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			content, err := os.ReadFile(filepath.Clean(path))
			require.NoError(t, err)
			for i, body := range strings.Split(string(content), "\n---\n") {
				if strings.TrimSpace(body) == "" {
					continue
				}
				docs = append(docs, presetDoc{name: fmt.Sprintf("%s[%d]", path, i), body: body})
			}
		}
	}
	require.NotEmpty(t, docs)
	return docs
}

// dropOmittedArgs walks a hand-written list of pod specs, so a pod spec added to
// the API later would let the marker through. Rendering every shipped preset
// catches that without putting reflection back into the controller.
func TestShippedPresetsNeverRenderTheOmitMarker(t *testing.T) {
	configs := []struct {
		name string
		cfg  *llmisvc.Config
	}{
		{name: "no TLS profile", cfg: &llmisvc.Config{}},
		{name: "TLS profile set", cfg: &llmisvc.Config{
			EnableTLS:              true,
			TLSMinVersion:          "VersionTLS12",
			TLSCipherSuites:        "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
			TLSCipherSuitesOpenSSL: "ECDHE-RSA-AES128-GCM-SHA256",
		}},
	}

	var rendered int
	for _, doc := range shippedPresetDocs(t) {
		preset := &v1alpha2.LLMInferenceServiceConfig{}
		require.NoError(t, yaml.Unmarshal([]byte(doc.body), preset), doc.name)
		if preset.Kind != "" && preset.Kind != "LLMInferenceServiceConfig" {
			continue
		}

		for _, tc := range configs {
			svc := &v1alpha2.LLMInferenceService{}
			svc.Name, svc.Namespace = "probe", "probe-ns"
			svc.Spec = preset.Spec

			out, err := llmisvc.ReplaceVariables(svc, preset, tc.cfg)
			if !assert.NoError(t, err, "%s with %s", doc.name, tc.name) {
				continue
			}

			asJSON, err := json.Marshal(out)
			require.NoError(t, err)
			assert.NotContains(t, string(asJSON), llmisvc.OmittedArgMarker,
				"%s rendered with %s still carries the omit marker, so a container would be started with it",
				doc.name, tc.name)
			rendered++
		}
	}
	require.NotZero(t, rendered, "no preset was rendered - the source directory moved?")
}

// The marker is only removed when it is the whole argv entry, so a preset must
// never bury it inside a longer string such as a bash -c script. Only the authored
// source is checked: the generated chart copy folds long lines, and it is derived
// from these files anyway.
func TestOmitMarkerIsAlwaysAWholeListEntry(t *testing.T) {
	standaloneEntry := regexp.MustCompile(`^\s*- '\{\{[^']*\}\}'$`)
	for _, dir := range presetSources(t)[:1] {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			content, err := os.ReadFile(filepath.Clean(path))
			require.NoError(t, err)
			for i, line := range strings.Split(string(content), "\n") {
				if !strings.Contains(line, llmisvc.OmittedArgMarker) {
					continue
				}
				assert.Regexp(t, standaloneEntry, line,
					"%s:%d puts the omit marker inside a larger value; only a whole argv entry is removed", path, i+1)
			}
		}
	}
}
