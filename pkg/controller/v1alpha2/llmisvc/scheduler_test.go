/*
Copyright 2025 The KServe Authors.

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

package llmisvc

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

func TestPreserveSchedulerConfig(t *testing.T) {
	defaultSvc := &v1alpha2.LLMInferenceService{}
	inlineSvc := &v1alpha2.LLMInferenceService{
		Spec: v1alpha2.LLMInferenceServiceSpec{
			Router: &v1alpha2.RouterSpec{
				Scheduler: &v1alpha2.SchedulerSpec{
					Config: &v1alpha2.SchedulerConfigSpec{
						Inline: &runtime.RawExtension{Raw: []byte("updated-inline-config")},
					},
				},
			},
		},
	}

	tests := []struct {
		name     string
		llmSvc   *v1alpha2.LLMInferenceService
		curr     *appsv1.Deployment
		expected []string
	}{
		{
			name:     "no current deployment - generates fresh config",
			llmSvc:   defaultSvc,
			curr:     &appsv1.Deployment{},
			expected: []string{"--config-text", schedulerConfigText(defaultSvc)},
		},
		{
			name:   "current deployment with --config-text - preserves it",
			llmSvc: defaultSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: []string{"--config-text", "existing-config-yaml"},
								},
							},
						},
					},
				},
			},
			expected: []string{"--config-text", "existing-config-yaml"},
		},
		{
			name:   "current deployment with -config-text - preserves it",
			llmSvc: defaultSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: []string{"-config-text", "old-config"},
								},
							},
						},
					},
				},
			},
			expected: []string{"-config-text", "old-config"},
		},
		{
			name:   "current deployment with --config-file - preserves it",
			llmSvc: defaultSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: []string{"--config-file", "/etc/scheduler/config.yaml"},
								},
							},
						},
					},
				},
			},
			expected: []string{"--config-file", "/etc/scheduler/config.yaml"},
		},
		{
			name:   "current deployment with non-main container - ignored",
			llmSvc: defaultSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "sidecar",
									Args: []string{"--config-text", "sidecar-config"},
								},
							},
						},
					},
				},
			},
			expected: []string{"--config-text", schedulerConfigText(defaultSvc)},
		},
		{
			name:   "inline config overrides existing deployment config",
			llmSvc: inlineSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: []string{"--config-text", "stale-config"},
								},
							},
						},
					},
				},
			},
			expected: []string{"--config-text", "updated-inline-config"},
		},
		{
			name:     "inline config used when no existing deployment",
			llmSvc:   inlineSvc,
			curr:     &appsv1.Deployment{},
			expected: []string{"--config-text", "updated-inline-config"},
		},
		{
			name: "template already has --config-text - returns nil to avoid duplication",
			llmSvc: &v1alpha2.LLMInferenceService{
				Spec: v1alpha2.LLMInferenceServiceSpec{
					Router: &v1alpha2.RouterSpec{
						Scheduler: &v1alpha2.SchedulerSpec{
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "main",
										Args: []string{"--config-text", "template-config", "--poolName", "test"},
									},
								},
							},
						},
					},
				},
			},
			curr:     &appsv1.Deployment{},
			expected: nil,
		},
		{
			name: "inline config overrides template config args",
			llmSvc: &v1alpha2.LLMInferenceService{
				Spec: v1alpha2.LLMInferenceServiceSpec{
					Router: &v1alpha2.RouterSpec{
						Scheduler: &v1alpha2.SchedulerSpec{
							Config: &v1alpha2.SchedulerConfigSpec{
								Inline: &runtime.RawExtension{Raw: []byte("inline-override")},
							},
							Template: &corev1.PodSpec{
								Containers: []corev1.Container{
									{
										Name: "main",
										Args: []string{"--config-text", "template-config"},
									},
								},
							},
						},
					},
				},
			},
			curr:     &appsv1.Deployment{},
			expected: []string{"--config-text", "inline-override"},
		},
		{
			name:   "config flag as last arg without value - generates fresh config",
			llmSvc: defaultSvc,
			curr: &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: []string{"--config-text"},
								},
							},
						},
					},
				},
			},
			expected: []string{"--config-text", schedulerConfigText(defaultSvc)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			result := preserveSchedulerConfig(tt.llmSvc, tt.curr)
			g.Expect(result).To(Equal(tt.expected))
		})
	}
}

func TestFilterArgs(t *testing.T) {
	tests := []struct {
		name              string
		args              []string
		names             map[string]bool
		expectedFiltered  []string
		expectedExtracted map[string]string
	}{
		{
			name:              "no matching args",
			args:              []string{"--poolName", "test-pool", "--grpc-port", "9002"},
			names:             map[string]bool{"kv-cache-usage-percentage-metric": true},
			expectedFiltered:  []string{"--poolName", "test-pool", "--grpc-port", "9002"},
			expectedExtracted: map[string]string{},
		},
		{
			name:              "remove flag with separate value",
			args:              []string{"--poolName", "test-pool", "--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc", "--grpc-port", "9002"},
			names:             map[string]bool{"kv-cache-usage-percentage-metric": true},
			expectedFiltered:  []string{"--poolName", "test-pool", "--grpc-port", "9002"},
			expectedExtracted: map[string]string{"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc"},
		},
		{
			name:              "remove flag with equals value",
			args:              []string{"--poolName", "test-pool", "--kv-cache-usage-percentage-metric=vllm:kv_cache_usage_perc", "--grpc-port", "9002"},
			names:             map[string]bool{"kv-cache-usage-percentage-metric": true},
			expectedFiltered:  []string{"--poolName", "test-pool", "--grpc-port", "9002"},
			expectedExtracted: map[string]string{"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc"},
		},
		{
			name: "remove multiple flags",
			args: []string{
				"--poolName", "test-pool",
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--grpc-port", "9002",
			},
			names: map[string]bool{
				"total-queued-requests-metric":     true,
				"total-running-requests-metric":    true,
				"kv-cache-usage-percentage-metric": true,
			},
			expectedFiltered: []string{"--poolName", "test-pool", "--grpc-port", "9002"},
			expectedExtracted: map[string]string{
				"total-queued-requests-metric":     "vllm:num_requests_waiting",
				"total-running-requests-metric":    "vllm:num_requests_running",
				"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc",
			},
		},
		{
			name:              "single dash prefix",
			args:              []string{"-kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc"},
			names:             map[string]bool{"kv-cache-usage-percentage-metric": true},
			expectedFiltered:  nil,
			expectedExtracted: map[string]string{"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc"},
		},
		{
			name:              "flag at end with no value",
			args:              []string{"--poolName", "test-pool", "--lora-info-metric"},
			names:             map[string]bool{"lora-info-metric": true},
			expectedFiltered:  []string{"--poolName", "test-pool"},
			expectedExtracted: map[string]string{"lora-info-metric": ""},
		},
		{
			name:              "flag followed by another flag (boolean-like)",
			args:              []string{"--lora-info-metric", "--grpc-port", "9002"},
			names:             map[string]bool{"lora-info-metric": true},
			expectedFiltered:  []string{"--grpc-port", "9002"},
			expectedExtracted: map[string]string{"lora-info-metric": ""},
		},
		{
			name:              "empty args",
			args:              []string{},
			names:             map[string]bool{"kv-cache-usage-percentage-metric": true},
			expectedFiltered:  nil,
			expectedExtracted: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)
			filtered, extracted := filterArgs(tt.args, tt.names)
			g.Expect(filtered).To(Equal(tt.expectedFiltered))
			g.Expect(extracted).To(Equal(tt.expectedExtracted))
		})
	}
}

func TestWithCoreMetricsExtractorPlugin(t *testing.T) {
	tests := []struct {
		name         string
		configYAML   string
		extracted    map[string]string
		validateFunc func(g Gomega, configObj map[string]interface{})
	}{
		{
			name: "injects plugin with extracted values",
			configYAML: `
plugins:
- type: queue-scorer
- type: max-score-picker
`,
			extracted: map[string]string{
				"total-queued-requests-metric":     "vllm:num_requests_waiting",
				"total-running-requests-metric":    "vllm:num_requests_running",
				"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc",
				"lora-info-metric":                 "vllm:lora_requests_info",
				"cache-info-metric":                "vllm:cache_config_info",
			},
			validateFunc: func(g Gomega, configObj map[string]interface{}) {
				plugins := configObj["plugins"].([]interface{})
				g.Expect(plugins).To(HaveLen(3))

				pluginMap := plugins[2].(map[string]interface{})
				g.Expect(pluginMap["name"]).To(Equal(coreMetricsExtractorPlugin))
				g.Expect(pluginMap["type"]).To(Equal(coreMetricsExtractorPlugin))

				params := pluginMap["parameters"].(map[string]interface{})
				g.Expect(params["engineLabelKey"]).To(Equal("inference.networking.k8s.io/engine-type"))
				g.Expect(params["defaultEngine"]).To(Equal("vllm"))

				engineConfigs := params["engineConfigs"].([]interface{})
				g.Expect(engineConfigs).To(HaveLen(1))

				engine := engineConfigs[0].(map[string]interface{})
				g.Expect(engine["name"]).To(Equal("vllm"))
				g.Expect(engine["queuedRequestsSpec"]).To(Equal("vllm:num_requests_waiting"))
				g.Expect(engine["runningRequestsSpec"]).To(Equal("vllm:num_requests_running"))
				g.Expect(engine["kvUsageSpec"]).To(Equal("vllm:kv_cache_usage_perc"))
				g.Expect(engine["loraSpec"]).To(Equal("vllm:lora_requests_info"))
				g.Expect(engine["cacheInfoSpec"]).To(Equal("vllm:cache_config_info"))
			},
		},
		{
			name: "skips injection when plugin already exists",
			configYAML: `
plugins:
- type: core-metrics-extractor
  name: core-metrics-extractor
  parameters:
    defaultEngine: vllm
`,
			extracted: map[string]string{
				"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc",
			},
			validateFunc: func(g Gomega, configObj map[string]interface{}) {
				plugins := configObj["plugins"].([]interface{})
				g.Expect(plugins).To(HaveLen(1), "should not duplicate plugin")
			},
		},
		{
			name: "skips injection when no values extracted",
			configYAML: `
plugins:
- type: queue-scorer
`,
			extracted: map[string]string{},
			validateFunc: func(g Gomega, configObj map[string]interface{}) {
				plugins := configObj["plugins"].([]interface{})
				g.Expect(plugins).To(HaveLen(1), "should not add plugin when no values extracted")
			},
		},
		{
			name: "injects plugin with partial values",
			configYAML: `
plugins:
- type: queue-scorer
`,
			extracted: map[string]string{
				"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc",
			},
			validateFunc: func(g Gomega, configObj map[string]interface{}) {
				plugins := configObj["plugins"].([]interface{})
				g.Expect(plugins).To(HaveLen(2))

				pluginMap := plugins[1].(map[string]interface{})
				params := pluginMap["parameters"].(map[string]interface{})
				engineConfigs := params["engineConfigs"].([]interface{})
				engine := engineConfigs[0].(map[string]interface{})
				g.Expect(engine["kvUsageSpec"]).To(Equal("vllm:kv_cache_usage_perc"))
				g.Expect(engine).NotTo(HaveKey("queuedRequestsSpec"))
				g.Expect(engine).NotTo(HaveKey("runningRequestsSpec"))
			},
		},
		{
			name: "injects plugin when no plugins field exists",
			configYAML: `
schedulingProfiles:
- name: default
`,
			extracted: map[string]string{
				"kv-cache-usage-percentage-metric": "vllm:kv_cache_usage_perc",
			},
			validateFunc: func(g Gomega, configObj map[string]interface{}) {
				plugins := configObj["plugins"].([]interface{})
				g.Expect(plugins).To(HaveLen(1))
				pluginMap := plugins[0].(map[string]interface{})
				g.Expect(pluginMap["type"]).To(Equal(coreMetricsExtractorPlugin))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)

			// Unmarshal into map directly to avoid unstructured.Unstructured
			// requiring Kind/apiVersion fields.
			var obj map[string]interface{}
			g.Expect(yaml.Unmarshal([]byte(tt.configYAML), &obj)).To(Succeed())

			u := unstructured.Unstructured{Object: obj}

			fn := withCoreMetricsExtractorPlugin(tt.extracted)
			g.Expect(fn(context.Background(), &u)).To(Succeed())

			tt.validateFunc(g, u.Object)
		})
	}
}

func TestRemoveSchedulerArg(t *testing.T) {
	// Config YAML needs apiVersion/kind to pass yaml.Unmarshal into unstructured.Unstructured
	// inside mutateSchedulerConfig.
	baseConfigYAML := `apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: queue-scorer
`

	tests := []struct {
		name           string
		version        string
		args           []string
		removeArgs     []string
		expectedArgs   []string
		validateConfig func(g Gomega, configText string)
	}{
		{
			name:    "removes args and injects plugin into config",
			version: "0.7.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--grpc-port", "9002",
			},
			removeArgs:   []string{"total-queued-requests-metric", "kv-cache-usage-percentage-metric"},
			expectedArgs: []string{"--config-text", "", "--grpc-port", "9002"},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(ContainSubstring("core-metrics-extractor"))
				g.Expect(configText).To(ContainSubstring("vllm:num_requests_waiting"))
				g.Expect(configText).To(ContainSubstring("vllm:kv_cache_usage_perc"))
			},
		},
		{
			name:    "removes equals-style args",
			version: "0.7.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--kv-cache-usage-percentage-metric=vllm:kv_cache_usage_perc",
				"--grpc-port", "9002",
			},
			removeArgs:   []string{"kv-cache-usage-percentage-metric"},
			expectedArgs: []string{"--config-text", "", "--grpc-port", "9002"},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(ContainSubstring("core-metrics-extractor"))
				g.Expect(configText).To(ContainSubstring("vllm:kv_cache_usage_perc"))
			},
		},
		{
			name:    "no matching args - no plugin injected",
			version: "0.7.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--grpc-port", "9002",
			},
			removeArgs:   []string{"total-queued-requests-metric"},
			expectedArgs: []string{"--config-text", "", "--grpc-port", "9002"},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).NotTo(ContainSubstring("core-metrics-extractor"))
			},
		},
		{
			name:    "removes all five metric args",
			version: "0.7.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			removeArgs: []string{
				"total-queued-requests-metric",
				"total-running-requests-metric",
				"kv-cache-usage-percentage-metric",
				"lora-info-metric",
				"cache-info-metric",
			},
			expectedArgs: []string{"--config-text", ""},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(ContainSubstring("core-metrics-extractor"))
				g.Expect(configText).To(ContainSubstring("queuedRequestsSpec"))
				g.Expect(configText).To(ContainSubstring("runningRequestsSpec"))
				g.Expect(configText).To(ContainSubstring("kvUsageSpec"))
				g.Expect(configText).To(ContainSubstring("loraSpec"))
				g.Expect(configText).To(ContainSubstring("cacheInfoSpec"))
			},
		},
		{
			name:    "removes all five metric args (0.8)",
			version: "0.8.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			removeArgs: []string{
				"total-queued-requests-metric",
				"total-running-requests-metric",
				"kv-cache-usage-percentage-metric",
				"lora-info-metric",
				"cache-info-metric",
			},
			expectedArgs: []string{"--config-text", ""},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(ContainSubstring("core-metrics-extractor"))
				g.Expect(configText).To(ContainSubstring("queuedRequestsSpec"))
				g.Expect(configText).To(ContainSubstring("runningRequestsSpec"))
				g.Expect(configText).To(ContainSubstring("kvUsageSpec"))
				g.Expect(configText).To(ContainSubstring("loraSpec"))
				g.Expect(configText).To(ContainSubstring("cacheInfoSpec"))
			},
		},
		{
			name:    "Leave args as is",
			version: "",
			args: []string{
				"--config-text", baseConfigYAML,
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			removeArgs: []string{},
			expectedArgs: []string{
				"--config-text", "",
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(Not(ContainSubstring("core-metrics-extractor")))
				g.Expect(configText).To(Not(ContainSubstring("queuedRequestsSpec")))
				g.Expect(configText).To(Not(ContainSubstring("runningRequestsSpec")))
				g.Expect(configText).To(Not(ContainSubstring("kvUsageSpec")))
				g.Expect(configText).To(Not(ContainSubstring("loraSpec")))
				g.Expect(configText).To(Not(ContainSubstring("cacheInfoSpec")))
			},
		},
		{
			name:    "Leave args as is (0.6.0)",
			version: "0.6.0",
			args: []string{
				"--config-text", baseConfigYAML,
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			removeArgs: []string{},
			expectedArgs: []string{
				"--config-text", "",
				"--total-queued-requests-metric", "vllm:num_requests_waiting",
				"--total-running-requests-metric", "vllm:num_requests_running",
				"--kv-cache-usage-percentage-metric", "vllm:kv_cache_usage_perc",
				"--lora-info-metric", "vllm:lora_requests_info",
				"--cache-info-metric", "vllm:cache_config_info",
			},
			validateConfig: func(g Gomega, configText string) {
				g.Expect(configText).To(ContainSubstring("queue-scorer"), "original plugins should be preserved")
				g.Expect(configText).To(Not(ContainSubstring("core-metrics-extractor")))
				g.Expect(configText).To(Not(ContainSubstring("queuedRequestsSpec")))
				g.Expect(configText).To(Not(ContainSubstring("runningRequestsSpec")))
				g.Expect(configText).To(Not(ContainSubstring("kvUsageSpec")))
				g.Expect(configText).To(Not(ContainSubstring("loraSpec")))
				g.Expect(configText).To(Not(ContainSubstring("cacheInfoSpec")))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewGomegaWithT(t)

			d := &appsv1.Deployment{
				Spec: appsv1.DeploymentSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								"app.kubernetes.io/version": tt.version,
							},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "main",
									Args: tt.args,
								},
							},
						},
					},
				},
			}

			transform := removeSchedulerArg(context.Background(), tt.removeArgs...)
			g.Expect(schedulerTransform(d, transform)).To(Succeed())

			mainContainer := d.Spec.Template.Spec.Containers[0]

			// Extract the config-text value for validation and replace with empty
			// string for args comparison.
			resultArgs := make([]string, len(mainContainer.Args))
			copy(resultArgs, mainContainer.Args)
			for i, a := range resultArgs {
				if a == "--config-text" && i+1 < len(resultArgs) {
					tt.validateConfig(g, resultArgs[i+1])
					resultArgs[i+1] = ""
				}
			}

			g.Expect(resultArgs).To(Equal(tt.expectedArgs))
		})
	}
}
