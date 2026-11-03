//go:build distro

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

package llmisvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
)

const (
	confidentialConfigSuffix       = "config-llm-confidential-bare-metal"
	confidentialConfigName         = "kserve-" + confidentialConfigSuffix
	confidentialDefaultsKey        = "confidentialBareMetal"
	confidentialHelpersName        = "kserve-confidential-bare-metal-resources"
	confidentialInitDataAnnotation = "io.katacontainers.config.hypervisor.cc_init_data"
	confidentialDiskKeyEnv         = "LUKS_RESOURCE_ID"
)

func init() {
	// Use the existing preset pinning lifecycle when the product prefix changes.
	WellKnownDefaultConfigs.Insert(configPrefix + confidentialConfigSuffix)
}

func confidentialPresetName(svc *v1alpha2.LLMInferenceService) string {
	return (&WellKnownConfigResolver{}).Resolve(svc, configPrefix+confidentialConfigSuffix)
}

// Only the installed preset can trigger automatic provisioning of SCC access.
// Its public name resolves through the same version pin as other product presets.
func (r *LLMISVCReconciler) getPlatformConfig(ctx context.Context, svc *v1alpha2.LLMInferenceService, name string) (*v1alpha2.LLMInferenceServiceConfig, bool, error) {
	installed := confidentialPresetName(svc)
	if name != confidentialConfigName && name != configPrefix+confidentialConfigSuffix && name != installed {
		return nil, false, nil
	}
	cfg := &v1alpha2.LLMInferenceServiceConfig{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: constants.KServeNamespace, Name: installed}, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, true, &configNotFoundError{Name: installed, Namespaces: []string{constants.KServeNamespace}}
		}
		return nil, true, fmt.Errorf("load installed confidential preset %s/%s: %w", constants.KServeNamespace, installed, err)
	}
	return cfg, true, nil
}

func usesConfidentialBareMetal(svc *v1alpha2.LLMInferenceService) bool {
	for _, ref := range svc.Status.AppliedConfigRefs {
		if string(ref.Namespace) == constants.KServeNamespace &&
			(string(ref.Name) == confidentialConfigName || string(ref.Name) == configPrefix+confidentialConfigSuffix || string(ref.Name) == confidentialPresetName(svc)) {
			return true
		}
	}
	return false
}

// Installation values only; the preset describes the pod and the SCC its permissions.
type confidentialDefaults struct {
	LUKSResourceID string `json:"luksResourceId"`
	InitData       string `json:"initData,omitempty"`
}

func (r *LLMISVCReconciler) loadConfidentialDefaults(ctx context.Context) (*confidentialDefaults, error) {
	cm := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: constants.KServeNamespace, Name: constants.InferenceServiceConfigMapName}, cm); err != nil {
		return nil, err
	}
	cfg := &confidentialDefaults{}
	if raw := cm.Data[confidentialDefaultsKey]; raw != "" {
		if err := json.Unmarshal([]byte(raw), cfg); err != nil {
			return nil, fmt.Errorf("inferenceservice-config.data.%s: %w", confidentialDefaultsKey, err)
		}
	}
	return cfg, nil
}

// The merged service template takes precedence over the installation default.
func configureConfidentialDiskKey(formatter *corev1.Container, fallback string) error {
	for _, env := range formatter.Env {
		if env.Name != confidentialDiskKeyEnv {
			continue
		}
		// Kubernetes resolves valueFrom when the container starts; the guest script
		// checks the resolved reference before accessing the disk.
		if env.ValueFrom != nil {
			return nil
		}
		if err := validateConfidentialDiskKey(env.Value); err != nil {
			return fmt.Errorf("spec.template.initContainers[name=format-disk].env[name=%s]: %w", confidentialDiskKeyEnv, err)
		}
		return nil
	}
	if err := validateConfidentialDiskKey(fallback); err != nil {
		return fmt.Errorf("inferenceservice-config.data.%s: %w", confidentialDefaultsKey, err)
	}
	formatter.Env = append(formatter.Env, corev1.EnvVar{Name: confidentialDiskKeyEnv, Value: fallback})
	return nil
}

func validateConfidentialDiskKey(key string) error {
	if key == "" {
		return errors.New("set LUKS_RESOURCE_ID on the format-disk init container or configure spec.confidentialContainers.luksResourceId on Kserve default-kserve with the key reference for the selected disk")
	}
	path, ok := strings.CutPrefix(key, "kbs:///")
	parts := strings.Split(path, "/")
	if !ok || len(parts) != 3 || strings.ContainsAny(path, " \t\r\n?#") {
		return errors.New("luksResourceId must identify the disk key as kbs:///repository/type/tag")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return errors.New("luksResourceId must identify the disk key as kbs:///repository/type/tag")
		}
	}
	return nil
}

// Record the installed name so existing preset watches and deletion protection work.
func platformConfigRefName(svc *v1alpha2.LLMInferenceService, cfg *v1alpha2.LLMInferenceServiceConfig, name string) string {
	if cfg.Namespace == constants.KServeNamespace && cfg.Name == confidentialPresetName(svc) {
		return cfg.Name
	}
	return name
}
