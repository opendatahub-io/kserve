package kservemodule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	odhLabels "github.com/opendatahub-io/odh-platform-utilities/pkg/metadata/labels"
)

// presetContentFingerprint returns a stable hash of the meaningful content of an
// LLMInferenceServiceConfig preset: its labels, annotations, and spec. The name and all
// server-managed metadata are excluded, so two versions of the same preset that differ
// only by version-prefixed name collide; a changed image (which lives in spec) does not.
//
// The operator-managed part-of label is excluded because it is constant across every
// preset and, unlike the other fields, is applied only after this comparison runs
// (commonPostRender), so including it would make a freshly rendered preset never match an
// already-deployed one.
func presetContentFingerprint(obj *unstructured.Unstructured) (string, error) {
	labels := map[string]string{}
	for k, v := range obj.GetLabels() {
		if k == odhLabels.PlatformPartOf {
			continue
		}
		labels[k] = v
	}

	spec, _, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil {
		return "", fmt.Errorf("reading spec of %s: %w", obj.GetName(), err)
	}

	payload := map[string]any{
		"labels":      labels,
		"annotations": obj.GetAnnotations(),
		"spec":        spec,
	}
	// encoding/json sorts map keys, so the marshalled form is deterministic.
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshalling content of %s: %w", obj.GetName(), err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// dedupeVersionedAcceleratorPresets drops rendered accelerator presets whose content is
// identical to an already-deployed preset carrying a different (prior-version) name. This
// avoids piling up a fresh versioned copy on patch releases where an accelerator's config
// and image are unchanged. Only accelerator presets (identified by isAcceleratorPreset) are
// considered; generic templates and other resources pass through untouched.
//
// It fails open: if existing presets cannot be listed, all rendered presets are kept.
func (r *KserveModuleReconciler) dedupeVersionedAcceleratorPresets(ctx context.Context,
	resources []unstructured.Unstructured) []unstructured.Unstructured {

	log := ctrl.LoggerFrom(ctx)

	hasAccelPreset := false
	for i := range resources {
		if isAcceleratorPreset(&resources[i]) {
			hasAccelPreset = true
			break
		}
	}
	if !hasAccelPreset {
		return resources
	}

	ns := r.getApplicationsNamespace()
	existing := &unstructured.UnstructuredList{}
	existing.SetGroupVersionKind(llmISVCConfigListGVK)
	if err := r.List(ctx, existing, client.InNamespace(ns)); err != nil {
		log.Error(err, "differential versioning: failed to list existing presets, keeping all")
		return resources
	}

	// fingerprint -> names of existing accelerator presets with that content.
	existingByFingerprint := make(map[string]map[string]struct{})
	for i := range existing.Items {
		item := &existing.Items[i]
		if item.GetNamespace() != ns || !isAcceleratorPreset(item) {
			continue
		}
		fp, err := presetContentFingerprint(item)
		if err != nil {
			log.Error(err, "differential versioning: skipping unreadable existing preset", "name", item.GetName())
			continue
		}
		if existingByFingerprint[fp] == nil {
			existingByFingerprint[fp] = make(map[string]struct{})
		}
		existingByFingerprint[fp][item.GetName()] = struct{}{}
	}

	filtered := make([]unstructured.Unstructured, 0, len(resources))
	var skipped []string
	for i := range resources {
		if !isAcceleratorPreset(&resources[i]) {
			filtered = append(filtered, resources[i])
			continue
		}

		fp, err := presetContentFingerprint(&resources[i])
		if err != nil {
			log.Error(err, "differential versioning: keeping preset with unreadable content", "name", resources[i].GetName())
			filtered = append(filtered, resources[i])
			continue
		}

		if hasContentTwinUnderDifferentName(existingByFingerprint[fp], resources[i].GetName()) {
			skipped = append(skipped, resources[i].GetName())
			continue
		}
		filtered = append(filtered, resources[i])
	}

	if len(skipped) > 0 {
		log.Info("differential versioning: skipped creating presets identical to an existing prior version",
			"count", len(skipped), "presets", skipped)
	}
	return filtered
}

// hasContentTwinUnderDifferentName reports whether the fingerprint bucket contains an
// existing preset with a name other than the candidate's, i.e. a content-identical prior
// version. A match on the same name only means the current version is already deployed and
// should keep being reconciled, so it does not count.
func hasContentTwinUnderDifferentName(names map[string]struct{}, candidate string) bool {
	for name := range names {
		if name != candidate {
			return true
		}
	}
	return false
}
