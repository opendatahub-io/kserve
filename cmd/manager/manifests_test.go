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

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func retirementWebhookObjects(t *testing.T) []*admissionregistrationv1.ValidatingWebhookConfiguration {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "config", "webhook", "manifests.yaml"))
	require.NoError(t, err)
	defer file.Close()
	decoder := yaml.NewYAMLOrJSONDecoder(file, 4096)
	var webhooks []*admissionregistrationv1.ValidatingWebhookConfiguration
	for {
		manifest := &admissionregistrationv1.ValidatingWebhookConfiguration{}
		err := decoder.Decode(manifest)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		if manifest.Name == "llminferenceservice.serving.kserve.io" || manifest.Name == "llminferenceserviceconfig.serving.kserve.io" {
			require.Equal(t, "ValidatingWebhookConfiguration", manifest.Kind)
			webhooks = append(webhooks, manifest)
		}
	}
	require.Len(t, webhooks, 2, "both existing retirement webhooks must remain installed")
	return webhooks
}

func TestWebhookManifestsRetainLLMInferenceServices(t *testing.T) {
	for _, configuration := range retirementWebhookObjects(t) {
		require.Len(t, configuration.Webhooks, 1)
		hook := configuration.Webhooks[0]
		require.Equal(t, admissionregistrationv1.Fail, *hook.FailurePolicy)
		require.NotNil(t, hook.ClientConfig.Service)
		require.Nil(t, hook.NamespaceSelector)
		require.Nil(t, hook.ObjectSelector)
		require.Len(t, hook.Rules, 1)
		rule := hook.Rules[0]
		require.Equal(t, []string{"serving.kserve.io"}, rule.APIGroups)
		require.Equal(t, []string{"v1alpha1"}, rule.APIVersions)
		require.Contains(t, rule.Operations, admissionregistrationv1.Create)
		require.Contains(t, rule.Operations, admissionregistrationv1.Update)
		if configuration.Name == "llminferenceservice.serving.kserve.io" {
			require.Equal(t, []string{"llminferenceservices"}, rule.Resources)
			require.Equal(t, "/validate-serving-kserve-io-v1alpha1-llminferenceservice", *hook.ClientConfig.Service.Path)
		} else {
			require.Equal(t, []string{"llminferenceserviceconfigs"}, rule.Resources)
			require.Equal(t, "/validate-serving-kserve-io-v1alpha1-llminferenceserviceconfig", *hook.ClientConfig.Service.Path)
		}
	}
}
