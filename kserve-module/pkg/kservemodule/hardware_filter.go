package kservemodule

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	platformv1alpha1 "github.com/opendatahub-io/kserve-module/pkg/apis/v1alpha1"
)

// acceleratorRequirements returns the accelerator resource names a preset targets,
// parsed from the opendatahub.io/recommended-accelerators annotation (a JSON array).
// It returns nil for non-accelerator presets (annotation absent) and for presets whose
// annotation is empty or unparseable, which are then treated as always-available.
func acceleratorRequirements(obj *unstructured.Unstructured) []string {
	raw := obj.GetAnnotations()[recommendedAcceleratorsAnnotationKey]
	if raw == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	// Drop empty entries so a malformed '[""]' does not filter everything out.
	out := names[:0]
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// resourceDomain returns the vendor domain of a Kubernetes resource name, i.e. the
// part before the first "/". For nvidia.com/gpu and nvidia.com/mig-1g.5gb this is
// "nvidia.com", so MIG-partitioned and vendor-variant devices satisfy a plain
// nvidia.com/gpu requirement. Names without a "/" (e.g. cpu, memory) have no domain.
func resourceDomain(name string) string {
	if before, _, ok := strings.Cut(name, "/"); ok {
		return before
	}
	return ""
}

// presentAcceleratorResources lists all nodes and collects the set of resource names
// and vendor domains that appear in any node's status.allocatable. The domains set lets
// a requirement match MIG/vendor-variant devices under the same domain.
func (r *KserveModuleReconciler) presentAcceleratorResources(ctx context.Context) (names, domains map[string]struct{}, err error) {
	nodes := &corev1.NodeList{}
	if err := r.List(ctx, nodes); err != nil {
		return nil, nil, fmt.Errorf("listing nodes: %w", err)
	}

	names = make(map[string]struct{})
	domains = make(map[string]struct{})
	for i := range nodes.Items {
		for resName := range nodes.Items[i].Status.Allocatable {
			name := resName.String()
			names[name] = struct{}{}
			if d := resourceDomain(name); d != "" {
				domains[d] = struct{}{}
			}
		}
	}
	return names, domains, nil
}

// presentDRADrivers lists Dynamic Resource Allocation ResourceSlices and returns the set of
// driver names publishing devices in the cluster (e.g. gpu.nvidia.com). ResourceSlices are
// the DRA analog of node status.allocatable: a driver only publishes them where its hardware
// is actually present, so they signal accelerator availability on DRA-based clusters.
//
// When the cluster does not serve the resource.k8s.io API (draResourceSliceGVK unset, decided
// at setup via the RESTMapper), it returns an empty set and no error. A genuine list error is
// returned so the caller can fail open.
func (r *KserveModuleReconciler) presentDRADrivers(ctx context.Context) (map[string]struct{}, error) {
	drivers := make(map[string]struct{})
	if r.draResourceSliceGVK.Empty() {
		return drivers, nil
	}

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   r.draResourceSliceGVK.Group,
		Version: r.draResourceSliceGVK.Version,
		Kind:    r.draResourceSliceGVK.Kind + "List",
	})
	if err := r.List(ctx, list); err != nil {
		return nil, fmt.Errorf("listing ResourceSlices: %w", err)
	}

	for i := range list.Items {
		driver, _, err := unstructured.NestedString(list.Items[i].Object, "spec", "driver")
		if err != nil || driver == "" {
			continue
		}
		drivers[driver] = struct{}{}
	}
	return drivers, nil
}

// acceleratorPresent reports whether an accelerator requirement is satisfied by the cluster:
//   - an exact allocatable match (device-plugin extended resources), or
//   - a match on the vendor domain (covering MIG devices such as nvidia.com/mig-1g.5gb
//     against a nvidia.com/gpu requirement), or
//   - a DRA ResourceSlice whose driver belongs to the same vendor domain (e.g. driver
//     gpu.nvidia.com satisfies a nvidia.com/gpu requirement).
func acceleratorPresent(req string, names, domains, draDrivers map[string]struct{}) bool {
	if _, ok := names[req]; ok {
		return true
	}
	d := resourceDomain(req)
	if d == "" {
		return false
	}
	if _, ok := domains[d]; ok {
		return true
	}
	for driver := range draDrivers {
		// gpu.nvidia.com (and compute-domain.nvidia.com, etc.) all end in ".nvidia.com";
		// a bare driver equal to the domain is matched too.
		if driver == d || strings.HasSuffix(driver, "."+d) {
			return true
		}
	}
	return false
}

// filterHardwareUnavailablePresets drops accelerator LLMInferenceServiceConfig presets
// whose required accelerator is not present in any node's status.allocatable. Presets
// without the recommended-accelerators annotation (generic templates, non-config kinds)
// are always kept. The behavior is disabled when spec.enableHardwareAwarePresets is false.
//
// It fails open: if the node list cannot be read, all presets are kept so a transient
// API error never blocks a rollout.
func (r *KserveModuleReconciler) filterHardwareUnavailablePresets(ctx context.Context,
	kserve *platformv1alpha1.Kserve, resources []unstructured.Unstructured) []unstructured.Unstructured {

	log := ctrl.LoggerFrom(ctx)

	if kserve == nil || !ptr.Deref(kserve.Spec.EnableHardwareAwarePresets, true) {
		return resources
	}

	// Cheap pre-check: skip the node list when nothing is a filterable accelerator preset.
	hasAccelPreset := false
	for i := range resources {
		if isAcceleratorPreset(&resources[i]) && len(acceleratorRequirements(&resources[i])) > 0 {
			hasAccelPreset = true
			break
		}
	}
	if !hasAccelPreset {
		return resources
	}

	names, domains, err := r.presentAcceleratorResources(ctx)
	if err != nil {
		log.Error(err, "hardware-aware filtering: failed to list nodes, keeping all presets")
		return resources
	}
	draDrivers, err := r.presentDRADrivers(ctx)
	if err != nil {
		log.Error(err, "hardware-aware filtering: failed to list DRA ResourceSlices, keeping all presets")
		return resources
	}

	filtered := make([]unstructured.Unstructured, 0, len(resources))
	var dropped []string
	for i := range resources {
		reqs := acceleratorRequirements(&resources[i])
		if len(reqs) == 0 || !isAcceleratorPreset(&resources[i]) {
			filtered = append(filtered, resources[i])
			continue
		}

		available := false
		for _, req := range reqs {
			if acceleratorPresent(req, names, domains, draDrivers) {
				available = true
				break
			}
		}
		if available {
			filtered = append(filtered, resources[i])
		} else {
			dropped = append(dropped, resources[i].GetName())
		}
	}

	if len(dropped) > 0 {
		log.Info("hardware-aware filtering: skipped accelerator presets with no matching node hardware",
			"count", len(dropped), "presets", dropped)
	}
	return filtered
}
