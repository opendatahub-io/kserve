/*
Copyright 2023 The KServe Authors.

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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kserveapis "github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

const (
	configPrefix              = "kserve-"
	configTemplateName        = configPrefix + "config-llm-template"
	configDecodeTemplateName  = configPrefix + "config-llm-decode-template"
	configWorkerName          = configPrefix + "config-llm-worker"
	configPrefillTemplateName = configPrefix + "config-llm-prefill-template"
	configPrefillWorkerName   = configPrefix + "config-llm-prefill-worker"
	configRouterSchedulerName = configPrefix + "config-llm-router-scheduler"
	configRouterRouteName     = configPrefix + "config-llm-router-route"
)

var wellKnownDefaultConfigs = sets.NewString(
	configTemplateName,
	configWorkerName,
	configPrefillTemplateName,
	configDecodeTemplateName,
	configPrefillWorkerName,
	configRouterSchedulerName,
	configRouterRouteName,
)

func (r *LLMInferenceServiceReconciler) combineBaseRefsConfig(ctx context.Context, llmSvc *kserveapis.LLMInferenceService) (*kserveapis.LLMInferenceServiceConfig, error) {
	// Apply well-known config overlays to inject default values for various components, when some components are
	// enabled. These LLMInferenceServiceConfig resources must exist in either resource namespace (prioritized) or
	// SystemNamespace (e.g. `kserve`).

	refs := make([]corev1.LocalObjectReference, 0, len(llmSvc.Spec.BaseRefs))
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil {
		refs = append(refs, corev1.LocalObjectReference{Name: configRouterSchedulerName})
	}
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Route != nil {
		refs = append(refs, corev1.LocalObjectReference{Name: configRouterRouteName})
	}
	if llmSvc.Spec.Worker != nil {
		refs = append(refs, corev1.LocalObjectReference{Name: configWorkerName})
	}
	if llmSvc.Spec.Prefill != nil {
		refs = append(refs, corev1.LocalObjectReference{Name: configPrefillTemplateName})
		refs = append(refs, corev1.LocalObjectReference{Name: configDecodeTemplateName})
	} else {
		refs = append(refs, corev1.LocalObjectReference{Name: configTemplateName})
	}
	if llmSvc.Spec.Prefill != nil && llmSvc.Spec.Prefill.Worker != nil {
		refs = append(refs, corev1.LocalObjectReference{Name: configPrefillWorkerName})
	}
	// Append explicit base refs to override well know configs.
	refs = append(refs, llmSvc.Spec.BaseRefs...)

	specs := make([]kserveapis.LLMInferenceServiceSpec, 0, len(llmSvc.Spec.BaseRefs)+1)
	for _, ref := range refs {
		cfg, err := r.getConfig(ctx, llmSvc, ref.Name)
		if err != nil {
			return nil, err
		}
		if cfg != nil {
			specs = append(specs, cfg.Spec)
		}
	}
	spec, err := MergeSpecs(specs...)
	if err != nil {
		return nil, fmt.Errorf("failed to merge specs: %w", err)
	}

	cfg := &kserveapis.LLMInferenceServiceConfig{
		ObjectMeta: *llmSvc.ObjectMeta.DeepCopy(),
		Spec:       spec,
	}
	return cfg, nil
}

// getConfig retrieves kserveapis.LLMInferenceServiceConfig with the given name from either the kserveapis.LLMInferenceService
// namespace or from the SystemNamespace (e.g. 'kserve'), prioritizing the former.
func (r *LLMInferenceServiceReconciler) getConfig(ctx context.Context, llmSvc *kserveapis.LLMInferenceService, name string) (*kserveapis.LLMInferenceServiceConfig, error) {
	cfg := &kserveapis.LLMInferenceServiceConfig{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: llmSvc.Namespace}, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			cfg = &kserveapis.LLMInferenceServiceConfig{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: r.Config.SystemNamespace}, cfg); err != nil {
				return nil, fmt.Errorf("failed to get LLMInferenceServiceConfig %q from namespaces [%q, %q]: %w", name, llmSvc.Namespace, "kserve", err)
			}
		}
	}
	return cfg, nil
}

func MergeSpecs(cfgs ...kserveapis.LLMInferenceServiceSpec) (kserveapis.LLMInferenceServiceSpec, error) {
	if len(cfgs) == 0 {
		return kserveapis.LLMInferenceServiceSpec{}, nil
	}

	out := cfgs[0]
	for i := 1; i < len(cfgs); i++ {
		cfg := cfgs[i]
		var err error
		out, err = mergeSpecs(out, cfg)
		if err != nil {
			return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("failed to merge specs: %w", err)
		}
	}
	return out, nil
}

// mergeSpecs performs a strategic merge by creating a clean patch from the override
// object and applying it to the base object.
func mergeSpecs(base, override kserveapis.LLMInferenceServiceSpec) (kserveapis.LLMInferenceServiceSpec, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal base spec: %w", err)
	}

	// To create a patch containing only the fields specified in the override,
	// we create a patch between a zero-valued ("empty") object and the override object.
	// This prevents zero-valued fields in the override struct (e.g., an empty string for an
	// unspecified image) from incorrectly wiping out values from the base.
	zero := kserveapis.LLMInferenceServiceSpec{}
	zeroJSON, err := json.Marshal(zero)
	if err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal zero spec: %w", err)
	}

	overrideJSON, err := json.Marshal(override)
	if err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal override spec: %w", err)
	}

	// Create the patch. It will only contain the non-default fields from the override.
	patch, err := strategicpatch.CreateTwoWayMergePatch(zeroJSON, overrideJSON, kserveapis.LLMInferenceServiceSpec{})
	if err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not create merge patch from override: %w", err)
	}

	// Apply this "clean" patch to the base JSON. The strategic merge logic will correctly
	// merge lists and objects based on their Kubernetes patch strategy annotations.
	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patch, kserveapis.LLMInferenceServiceSpec{})
	if err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not apply merge patch: %w", err)
	}

	// Unmarshal the merged JSON back into a Go struct.
	var finalSpec kserveapis.LLMInferenceServiceSpec
	if err := json.Unmarshal(mergedJSON, &finalSpec); err != nil {
		return kserveapis.LLMInferenceServiceSpec{}, fmt.Errorf("could not unmarshal merged spec: %w", err)
	}
	return finalSpec, nil
}
