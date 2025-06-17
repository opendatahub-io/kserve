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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

func MergeSpecs(cfgs ...v1alpha1.LLMInferenceServiceSpec) (v1alpha1.LLMInferenceServiceSpec, error) {
	if len(cfgs) == 0 {
		return v1alpha1.LLMInferenceServiceSpec{}, nil
	}

	out := cfgs[0]
	for i := 1; i < len(cfgs); i++ {
		cfg := cfgs[i]
		var err error
		out, err = mergeSpecs(out, cfg)
		if err != nil {
			return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("failed to merge specs: %w", err)
		}
	}
	return out, nil
}

// mergeSpecs performs a strategic merge by creating a clean patch from the override
// object and applying it to the base object.
func mergeSpecs(base, override v1alpha1.LLMInferenceServiceSpec) (v1alpha1.LLMInferenceServiceSpec, error) {
	baseJSON, err := json.Marshal(base)
	if err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal base spec: %w", err)
	}

	// To create a patch containing only the fields specified in the override,
	// we create a patch between a zero-valued ("empty") object and the override object.
	// This prevents zero-valued fields in the override struct (e.g., an empty string for an
	// unspecified image) from incorrectly wiping out values from the base.
	zero := v1alpha1.LLMInferenceServiceSpec{}
	zeroJSON, err := json.Marshal(zero)
	if err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal zero spec: %w", err)
	}

	overrideJSON, err := json.Marshal(override)
	if err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not marshal override spec: %w", err)
	}

	// Create the patch. It will only contain the non-default fields from the override.
	patch, err := strategicpatch.CreateTwoWayMergePatch(zeroJSON, overrideJSON, v1alpha1.LLMInferenceServiceSpec{})
	if err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not create merge patch from override: %w", err)
	}

	// Apply this "clean" patch to the base JSON. The strategic merge logic will correctly
	// merge lists and objects based on their Kubernetes patch strategy annotations.
	mergedJSON, err := strategicpatch.StrategicMergePatch(baseJSON, patch, v1alpha1.LLMInferenceServiceSpec{})
	if err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not apply merge patch: %w", err)
	}

	// Unmarshal the merged JSON back into a Go struct.
	var finalSpec v1alpha1.LLMInferenceServiceSpec
	if err := json.Unmarshal(mergedJSON, &finalSpec); err != nil {
		return v1alpha1.LLMInferenceServiceSpec{}, fmt.Errorf("could not unmarshal merged spec: %w", err)
	}

	return finalSpec, nil
}

func (r *LLMInferenceServiceReconciler) combineBaseRefsConfig(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) (*v1alpha1.LLMInferenceServiceConfig, error) {
	if len(llmSvc.Spec.BaseRefs) == 0 {
		var err error
		cfg, err := r.getConfig(ctx, llmSvc, "llm-d")
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg := &v1alpha1.LLMInferenceServiceConfig{}
	for _, ref := range llmSvc.Spec.BaseRefs {
		ov, err := r.getConfig(ctx, llmSvc, ref.Name)
		if err != nil {
			return nil, err
		}

		spec, err := mergeSpecs(cfg.Spec, ov.Spec)
		if err != nil {
			return nil, fmt.Errorf("failed to merge specs: %w", err)
		}
		cfg.Spec = spec
	}
	return cfg, nil
}

// getConfig retrieves v1alpha1.LLMInferenceServiceConfig with the given name from either the v1alpha1.LLMInferenceService
// namespace or from the SystemNamespace (e.g. 'kserve'), prioritizing the former.
func (r *LLMInferenceServiceReconciler) getConfig(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, name string) (*v1alpha1.LLMInferenceServiceConfig, error) {
	cfg := &v1alpha1.LLMInferenceServiceConfig{}
	if err := r.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: llmSvc.Namespace}, cfg); err != nil {
		if apierrors.IsNotFound(err) {
			cfg = &v1alpha1.LLMInferenceServiceConfig{}
			if err := r.Client.Get(ctx, client.ObjectKey{Name: name, Namespace: r.Config.SystemNamespace}, cfg); err != nil {
				return nil, fmt.Errorf("failed to get LLMInferenceServiceConfig %q from namespaces [%q, %q]: %w", name, llmSvc.Namespace, "kserve", err)
			}
		}
	}
	return cfg, nil
}
