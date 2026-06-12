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

// Package hwprofile provides utilities for resolving ODH HardwareProfile CRs and
// applying the resolved scheduling stanzas to Kubernetes workloads.
package hwprofile

import (
	"context"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/kserve/kserve/pkg/constants"
)

// ResolvedProfile contains the parsed scheduling stanzas from a HardwareProfile CR.
type ResolvedProfile struct {
	Identifiers    []ResourceIdentifier
	NodeSelector   map[string]string
	Tolerations    []corev1.Toleration
	KueueQueueName string // empty when scheduling type is not "Queue"
}

// ResourceIdentifier maps a HardwareProfile resource identifier to a quantity.
type ResourceIdentifier struct {
	ResourceName corev1.ResourceName
	DefaultCount resource.Quantity
}

var hardwareProfileGVK = schema.GroupVersionKind{
	Group:   constants.HardwareProfileGroup,
	Version: constants.HardwareProfileVersion,
	Kind:    "HardwareProfile",
}

// Resolve fetches the named HardwareProfile CR and returns the parsed ResolvedProfile.
//
// Returns nil, nil when name is empty (no annotation present). Returns an error
// (including NotFound) when the CR cannot be fetched, causing the caller to abort
// reconciliation.
//
// Parameters:
//   - ctx: Request context
//   - c: controller-runtime client (unstructured read)
//   - name: HardwareProfile CR name
//   - namespace: HardwareProfile CR namespace
func Resolve(ctx context.Context, c client.Client, name, namespace string) (*ResolvedProfile, error) {
	if name == "" {
		return nil, nil
	}

	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(hardwareProfileGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, obj); err != nil {
		return nil, fmt.Errorf("failed fetching HardwareProfile %s/%s: %w", namespace, name, err)
	}

	return parseProfile(ctx, obj)
}

// parseProfile extracts the scheduling stanzas from a HardwareProfile unstructured object.
func parseProfile(ctx context.Context, obj *unstructured.Unstructured) (*ResolvedProfile, error) {
	logger := log.FromContext(ctx)
	profile := &ResolvedProfile{}

	// Parse identifiers from spec.identifiers[]
	identifiersRaw, _, _ := unstructured.NestedSlice(obj.Object, "spec", "identifiers")
	for _, item := range identifiersRaw {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		identifier, _, _ := unstructured.NestedString(m, "identifier")
		if identifier == "" {
			continue
		}
		defaultCountStr, _, _ := unstructured.NestedString(m, "defaultCount")
		var qty resource.Quantity
		if defaultCountStr != "" {
			var err error
			qty, err = resource.ParseQuantity(defaultCountStr)
			if err != nil {
				logger.V(1).Info("failed to parse defaultCount, skipping identifier",
					"identifier", identifier, "defaultCount", defaultCountStr, "error", err)
				continue
			}
		} else {
			qty = *resource.NewQuantity(1, resource.DecimalSI)
		}
		profile.Identifiers = append(profile.Identifiers, ResourceIdentifier{
			ResourceName: corev1.ResourceName(identifier),
			DefaultCount: qty,
		})
	}

	// Parse scheduling spec
	schedulingType, _, _ := unstructured.NestedString(obj.Object, "spec", "schedulingSpec", "type")
	switch schedulingType {
	case "Queue":
		queueName, _, _ := unstructured.NestedString(obj.Object, "spec", "schedulingSpec", "kueue", "localQueueName")
		profile.KueueQueueName = queueName

	case "Node":
		nodeSelector, _, _ := unstructured.NestedStringMap(obj.Object, "spec", "schedulingSpec", "node", "nodeSelector")
		profile.NodeSelector = nodeSelector

		tolerationsRaw, _, _ := unstructured.NestedSlice(obj.Object, "spec", "schedulingSpec", "node", "tolerations")
		for _, item := range tolerationsRaw {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			tol := corev1.Toleration{}
			if v, ok := m["key"].(string); ok {
				tol.Key = v
			}
			if v, ok := m["operator"].(string); ok {
				tol.Operator = corev1.TolerationOperator(v)
			}
			if v, ok := m["value"].(string); ok {
				tol.Value = v
			}
			if v, ok := m["effect"].(string); ok {
				tol.Effect = corev1.TaintEffect(v)
			}
			if v, ok := m["tolerationSeconds"]; ok {
				switch ts := v.(type) {
				case int64:
					tol.TolerationSeconds = &ts
				case float64:
					s := int64(ts)
					tol.TolerationSeconds = &s
				}
			}
			profile.Tolerations = append(profile.Tolerations, tol)
		}
	}

	return profile, nil
}

// ApplyToContainerResources injects HardwareProfile resource identifiers into the named container.
//
// Each identifier is applied only when that resource type is not already present in the
// container's requests or limits. Requests equal limits (Guaranteed QoS). A missing
// container is logged as a warning; the function does not fail.
//
// Parameters:
//   - ctx: Request context (for logging)
//   - profile: Resolved HardwareProfile
//   - containerName: Target container name
//   - podSpec: The pod spec to modify in-place
func ApplyToContainerResources(ctx context.Context, profile *ResolvedProfile, containerName string, podSpec *corev1.PodSpec) {
	if profile == nil || len(profile.Identifiers) == 0 {
		return
	}

	logger := log.FromContext(ctx)

	idx := -1
	for i, c := range podSpec.Containers {
		if c.Name == containerName {
			idx = i
			break
		}
	}
	if idx == -1 {
		logger.V(1).Info("HWP container not found, skipping resource injection", "container", containerName)
		return
	}

	c := &podSpec.Containers[idx]
	if c.Resources.Requests == nil {
		c.Resources.Requests = make(corev1.ResourceList)
	}
	if c.Resources.Limits == nil {
		c.Resources.Limits = make(corev1.ResourceList)
	}

	for _, id := range profile.Identifiers {
		// Skip if already set in requests or limits
		if _, ok := c.Resources.Requests[id.ResourceName]; ok {
			continue
		}
		if _, ok := c.Resources.Limits[id.ResourceName]; ok {
			continue
		}
		c.Resources.Requests[id.ResourceName] = id.DefaultCount
		c.Resources.Limits[id.ResourceName] = id.DefaultCount
	}
}

// ApplyNodeScheduling merges HardwareProfile node selector and toleration entries into the pod spec.
//
// Existing pod spec entries take priority: node selector keys already present are not
// overwritten; tolerations already present (matched by key+operator+value+effect+tolerationSeconds)
// are not duplicated.
//
// Parameters:
//   - profile: Resolved HardwareProfile
//   - podSpec: The pod spec to modify in-place
func ApplyNodeScheduling(profile *ResolvedProfile, podSpec *corev1.PodSpec) {
	if profile == nil {
		return
	}

	// Merge node selector: existing entries take priority
	if len(profile.NodeSelector) > 0 {
		if podSpec.NodeSelector == nil {
			podSpec.NodeSelector = make(map[string]string)
		}
		for k, v := range profile.NodeSelector {
			if _, exists := podSpec.NodeSelector[k]; !exists {
				podSpec.NodeSelector[k] = v
			}
		}
	}

	// Merge tolerations: avoid duplicates
	if len(profile.Tolerations) > 0 {
		existing := tolerationKeySet(podSpec.Tolerations)
		for _, tol := range profile.Tolerations {
			key := tolerationKey(tol)
			if !existing[key] {
				podSpec.Tolerations = append(podSpec.Tolerations, tol)
				existing[key] = true
			}
		}
	}
}

// ApplyKueueLabel sets the kueue.x-k8s.io/queue-name label on the provided ObjectMeta only
// when the label is not already set and the profile specifies a queue name.
//
// Parameters:
//   - profile: Resolved HardwareProfile
//   - meta: ObjectMeta to modify in-place
func ApplyKueueLabel(profile *ResolvedProfile, meta *metav1.ObjectMeta) {
	if profile == nil || profile.KueueQueueName == "" {
		return
	}
	if meta.Labels == nil {
		meta.Labels = make(map[string]string)
	}
	if _, exists := meta.Labels[constants.KueueQueueNameLabel]; !exists {
		meta.Labels[constants.KueueQueueNameLabel] = profile.KueueQueueName
	}
}

// HardwareProfileRef extracts the HardwareProfile name and namespace from workload annotations.
// When the namespace annotation is absent, defaultNamespace is used.
//
// Returns ("", "") when the name annotation is absent, indicating no HWP injection.
//
// Parameters:
//   - annotations: Workload annotation map
//   - defaultNamespace: Fallback namespace when annotation is absent
func HardwareProfileRef(annotations map[string]string, defaultNamespace string) (name, namespace string) {
	name = annotations[constants.HardwareProfileAnnotationName]
	namespace = annotations[constants.HardwareProfileAnnotationNamespace]
	if namespace == "" {
		namespace = defaultNamespace
	}
	return
}

// AnnotationChanged reports whether the HWP annotation on the workload differs from the
// annotation recorded on an existing owned resource (Deployment or LWS).
//
// Uses HardwareProfileRef on both sides so the optional namespace annotation is normalised
// identically to how resolution works, preventing false positives.
//
// Parameters:
//   - workloadAnnotations: Annotations from the IS or LLMis
//   - workloadNamespace: Namespace of the IS or LLMis (used as default for missing namespace annotation)
//   - existingAnnotations: Annotations from the existing Deployment or LWS
func AnnotationChanged(workloadAnnotations map[string]string, workloadNamespace string, existingAnnotations map[string]string) bool {
	wName, wNS := HardwareProfileRef(workloadAnnotations, workloadNamespace)
	eName, eNS := HardwareProfileRef(existingAnnotations, workloadNamespace)
	return wName != eName || wNS != eNS
}

// CopyContainerResources copies resource types from the named container in src into the
// matching container in dst, skipping types already present in dst (in either requests or
// limits). Requests and limits are set to the same value (Guaranteed QoS), mirroring the
// Apply* semantics.
//
// A missing container in dst is logged as a warning; the function does not fail.
//
// Parameters:
//   - ctx: Request context (for logging)
//   - containerName: Name of the target container in both pod specs
//   - src: Source pod spec (e.g. from an existing Deployment)
//   - dst: Destination pod spec to modify in-place
func CopyContainerResources(ctx context.Context, containerName string, src, dst *corev1.PodSpec) {
	if src == nil {
		return
	}
	logger := log.FromContext(ctx)

	srcIdx := -1
	for i, c := range src.Containers {
		if c.Name == containerName {
			srcIdx = i
			break
		}
	}
	if srcIdx == -1 {
		return
	}

	dstIdx := -1
	for i, c := range dst.Containers {
		if c.Name == containerName {
			dstIdx = i
			break
		}
	}
	if dstIdx == -1 {
		logger.V(1).Info("HWP frozen-mode: container not found in destination, skipping resource copy", "container", containerName)
		return
	}

	srcContainer := &src.Containers[srcIdx]
	dstContainer := &dst.Containers[dstIdx]

	if dstContainer.Resources.Requests == nil {
		dstContainer.Resources.Requests = make(corev1.ResourceList)
	}
	if dstContainer.Resources.Limits == nil {
		dstContainer.Resources.Limits = make(corev1.ResourceList)
	}

	for resourceName, qty := range srcContainer.Resources.Requests {
		if _, inRequests := dstContainer.Resources.Requests[resourceName]; inRequests {
			continue
		}
		if _, inLimits := dstContainer.Resources.Limits[resourceName]; inLimits {
			continue
		}
		copied := qty.DeepCopy()
		dstContainer.Resources.Requests[resourceName] = copied
		dstContainer.Resources.Limits[resourceName] = copied
	}
}

// CopyNodeScheduling copies nodeSelector keys and tolerations from src into dst,
// skipping entries already present in dst. Existing dst entries take priority.
//
// Parameters:
//   - src: Source pod spec (e.g. from an existing Deployment)
//   - dst: Destination pod spec to modify in-place
func CopyNodeScheduling(src, dst *corev1.PodSpec) {
	if src == nil {
		return
	}

	if len(src.NodeSelector) > 0 {
		if dst.NodeSelector == nil {
			dst.NodeSelector = make(map[string]string)
		}
		for k, v := range src.NodeSelector {
			if _, exists := dst.NodeSelector[k]; !exists {
				dst.NodeSelector[k] = v
			}
		}
	}

	if len(src.Tolerations) > 0 {
		existing := tolerationKeySet(dst.Tolerations)
		for _, tol := range src.Tolerations {
			key := tolerationKey(tol)
			if !existing[key] {
				dst.Tolerations = append(dst.Tolerations, tol)
				existing[key] = true
			}
		}
	}
}

// CopyKueueLabel copies the kueue.x-k8s.io/queue-name label from srcMeta into dstMeta
// only when the label is not already set in dst.
//
// Parameters:
//   - srcMeta: Source ObjectMeta (e.g. from an existing Deployment)
//   - dstMeta: Destination ObjectMeta to modify in-place
func CopyKueueLabel(srcMeta, dstMeta *metav1.ObjectMeta) {
	if srcMeta == nil || dstMeta == nil {
		return
	}
	queueName, ok := srcMeta.Labels[constants.KueueQueueNameLabel]
	if !ok || queueName == "" {
		return
	}
	if dstMeta.Labels == nil {
		dstMeta.Labels = make(map[string]string)
	}
	if _, exists := dstMeta.Labels[constants.KueueQueueNameLabel]; !exists {
		dstMeta.Labels[constants.KueueQueueNameLabel] = queueName
	}
}

// tolerationKey generates a deduplication key for a toleration.
func tolerationKey(tol corev1.Toleration) string {
	ts := ""
	if tol.TolerationSeconds != nil {
		ts = strconv.FormatInt(*tol.TolerationSeconds, 10)
	}
	return fmt.Sprintf("%s:%s:%s:%s:%s", tol.Key, tol.Operator, tol.Value, tol.Effect, ts)
}

// tolerationKeySet builds a set of deduplication keys from a slice of tolerations.
func tolerationKeySet(tolerations []corev1.Toleration) map[string]bool {
	s := make(map[string]bool, len(tolerations))
	for _, t := range tolerations {
		s[tolerationKey(t)] = true
	}
	return s
}
