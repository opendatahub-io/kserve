//go:build distro

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
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/env"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// sccDisabled indicates whether SCC role binding reconciliation is disabled at runtime.
// When set to "true", the controller will skip creating SCC RoleBinding resources,
// useful for environments where SecurityContextConstraints are not needed.
var sccDisabled, _ = env.GetBool("LLMISVC_SCC_DISABLED", false)

const llmInferenceServiceSCCClusterRole = "openshift-ai-llminferenceservice-scc"

func (r *LLMISVCReconciler) reconcileWorkloadPlatformPermissions(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	if sccDisabled {
		log.FromContext(ctx).V(2).Info("SCC is disabled via LLMISVC_SCC_DISABLED, skipping SCC role binding reconciliation")
		return nil
	}
	if err := r.reconcileMultiNodeSCCRoleBinding(ctx, llmSvc); err != nil {
		return fmt.Errorf("failed to reconcile multi-node SCC role binding: %w", err)
	}
	if err := r.reconcileSingleNodeSCCRoleBinding(ctx, llmSvc, config); err != nil {
		return fmt.Errorf("failed to reconcile single-node SCC role binding: %w", err)
	}
	return nil
}

func (r *LLMISVCReconciler) reconcileMultiNodeSCCRoleBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected, err := r.expectedMultiNodeSCCRoleBinding(ctx, llmSvc)
	if err != nil {
		return fmt.Errorf("failed to create expected multi node scc role binding: %w", err)
	}
	if utils.GetForceStopRuntime(llmSvc) || (llmSvc.Spec.Worker == nil && (llmSvc.Spec.Prefill == nil || llmSvc.Spec.Prefill.Worker == nil)) {
		return Delete(ctx, r, llmSvc, expected)
	}
	return Reconcile(ctx, r, llmSvc, &rbacv1.RoleBinding{}, expected, semanticRoleBindingIsEqual)
}

func (r *LLMISVCReconciler) expectedMultiNodeSCCRoleBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) (*rbacv1.RoleBinding, error) {
	m, _, err := r.expectedMultiNodeMainServiceAccount(ctx, llmSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create expected multi node main service account: %w", err)
	}
	p, _, err := r.expectedMultiNodePrefillServiceAccount(ctx, llmSvc)
	if err != nil {
		return nil, fmt.Errorf("failed to create expected multi node prefill service account: %w", err)
	}

	expected := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-mn-scc"),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
				constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     llmInferenceServiceSCCClusterRole,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: m.Name,
			},
			{
				Kind: "ServiceAccount",
				Name: p.Name,
			},
		},
	}

	log.FromContext(ctx).V(2).Info("Expected SCC multi-node role binding", "rolebinding", expected)

	return expected, nil
}

// reconcileSingleNodeSCCRoleBinding grants the SCC to the ServiceAccounts of the
// single-node pods that add a capability restricted-v2 does not allow. The namespace
// default ServiceAccount is never bound.
func (r *LLMISVCReconciler) reconcileSingleNodeSCCRoleBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, config *Config) error {
	expected := expectedSingleNodeSCCRoleBinding(llmSvc)
	if utils.GetForceStopRuntime(llmSvc) {
		return r.deleteOwnedRoleBinding(ctx, llmSvc, expected)
	}

	pods, err := r.singleNodeWorkloadPodSpecs(ctx, llmSvc, config)
	if err != nil {
		return fmt.Errorf("failed to render single-node pod specs: %w", err)
	}
	for i := range pods {
		name := pods[i].ServiceAccountName
		if name == "" || name == constants.LLMISVCDefaultServiceAccountName || !addsCapabilityRejectedByRestrictedV2(&pods[i]) {
			continue
		}
		if !slices.ContainsFunc(expected.Subjects, func(s rbacv1.Subject) bool { return s.Name == name }) {
			expected.Subjects = append(expected.Subjects, rbacv1.Subject{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      name,
				Namespace: llmSvc.GetNamespace(),
			})
		}
	}

	if len(expected.Subjects) == 0 {
		return r.deleteOwnedRoleBinding(ctx, llmSvc, expected)
	}
	return Reconcile(ctx, r, llmSvc, &rbacv1.RoleBinding{}, expected, semanticSCCRoleBindingIsEqual)
}

// deleteOwnedRoleBinding deletes the RoleBinding only when llmSvc controls it.
func (r *LLMISVCReconciler) deleteOwnedRoleBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, expected *rbacv1.RoleBinding) error {
	curr := &rbacv1.RoleBinding{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(expected), curr); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(curr, llmSvc) {
		return nil
	}
	return Delete(ctx, r, llmSvc, expected)
}

// requiresDedicatedServiceAccount reports whether a single-node pod that names no
// ServiceAccount runs as <name>-kserve: true when it adds a capability restricted-v2 rejects.
func requiresDedicatedServiceAccount(pod *corev1.PodSpec) bool {
	return addsCapabilityRejectedByRestrictedV2(pod)
}

// platformNeedsSingleNodeServiceAccount reports whether the dedicated single-node
// ServiceAccount name must exist: a single-node template that names no ServiceAccount
// requires it, or a deployed single-node pod runs as it.
func (r *LLMISVCReconciler) platformNeedsSingleNodeServiceAccount(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, name string) (bool, error) {
	if singleNodeTemplatesRequireDedicatedServiceAccount(llmSvc) {
		return true, nil
	}
	pods, err := r.deployedSingleNodePodSpecs(ctx, llmSvc)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(pods, func(pod corev1.PodSpec) bool { return pod.ServiceAccountName == name }), nil
}

func singleNodeTemplatesRequireDedicatedServiceAccount(llmSvc *v1alpha2.LLMInferenceService) bool {
	var templates []*corev1.PodSpec
	if llmSvc.Spec.Worker == nil {
		templates = append(templates, llmSvc.Spec.Template)
	}
	if llmSvc.Spec.Prefill != nil && llmSvc.Spec.Prefill.Worker == nil {
		templates = append(templates, llmSvc.Spec.Prefill.Template)
	}
	return slices.ContainsFunc(templates, func(pod *corev1.PodSpec) bool {
		return pod != nil && pod.ServiceAccountName == "" && requiresDedicatedServiceAccount(pod)
	})
}

// deployedSingleNodePodSpecs returns the pod specs of the single-node main and prefill
// Deployments and DisaggregatedSet roles that exist.
func (r *LLMISVCReconciler) deployedSingleNodePodSpecs(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) ([]corev1.PodSpec, error) {
	var pods []corev1.PodSpec
	for _, name := range []string{mainDeploymentName(llmSvc), prefillDeploymentName(llmSvc)} {
		pod, err := r.deployedDeploymentPodSpec(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: name})
		if err != nil {
			return nil, fmt.Errorf("failed to get deployment %s/%s: %w", llmSvc.GetNamespace(), name, err)
		}
		pods = append(pods, pod)
	}
	if !r.DisaggregatedSetAvailable {
		return pods, nil
	}
	ds, err := r.currentDisaggregatedSet(ctx, llmSvc)
	if err != nil {
		return nil, err
	}
	if ds != nil {
		for _, role := range []string{constants.LLMDRoleDecode, constants.LLMDRolePrefill} {
			pods = append(pods, deployedDisaggregatedRole(ds, role).Worker)
		}
	}
	return pods, nil
}

// singleNodeWorkloadPodSpecs renders the single-node main and prefill pod specs the way
// reconcileSingleNodeWorkload or reconcileDisaggregatedSet deploys them.
func (r *LLMISVCReconciler) singleNodeWorkloadPodSpecs(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, config *Config) ([]corev1.PodSpec, error) {
	useDisaggregatedSet := useDisaggregatedSetWorkload(llmSvc, r.decideDisaggregatedSet(llmSvc, config))
	var ds *disaggregatedsetv1.DisaggregatedSet
	if useDisaggregatedSet {
		var err error
		if ds, err = r.currentDisaggregatedSet(ctx, llmSvc); err != nil {
			return nil, err
		}
	}
	deployed := func(role, deploymentName string) (corev1.PodSpec, error) {
		if useDisaggregatedSet {
			return deployedDisaggregatedRole(ds, role).Worker, nil
		}
		return r.deployedDeploymentPodSpec(ctx, types.NamespacedName{Namespace: llmSvc.GetNamespace(), Name: deploymentName})
	}

	var pods []corev1.PodSpec
	if llmSvc.Spec.Worker == nil && llmSvc.Spec.Template != nil {
		current, err := deployed(constants.LLMDRoleDecode, mainDeploymentName(llmSvc))
		if err != nil {
			return nil, err
		}
		pod, err := r.expectedSingleNodeMainPodTemplate(ctx, llmSvc, config, current)
		if err != nil {
			return nil, err
		}
		pods = append(pods, pod.Template.Spec)
	}
	if prefill := llmSvc.Spec.Prefill; prefill != nil && prefill.Worker == nil && prefill.Template != nil {
		current, err := deployed(constants.LLMDRolePrefill, prefillDeploymentName(llmSvc))
		if err != nil {
			return nil, err
		}
		pod, err := r.expectedSingleNodePrefillPodTemplate(ctx, llmSvc, config, current)
		if err != nil {
			return nil, err
		}
		pods = append(pods, pod.Template.Spec)
	}
	return pods, nil
}

func addsCapabilityRejectedByRestrictedV2(pod *corev1.PodSpec) bool {
	for _, containers := range [][]corev1.Container{pod.InitContainers, pod.Containers} {
		for i := range containers {
			sc := containers[i].SecurityContext
			if sc == nil || sc.Capabilities == nil {
				continue
			}
			if slices.ContainsFunc(sc.Capabilities.Add, func(c corev1.Capability) bool { return c != "NET_BIND_SERVICE" }) {
				return true
			}
		}
	}
	return false
}

func expectedSingleNodeSCCRoleBinding(llmSvc *v1alpha2.LLMInferenceService) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-scc"),
			Namespace: llmSvc.GetNamespace(),
			Labels: map[string]string{
				constants.KubernetesAppNameLabelKey: llmSvc.GetName(),
				constants.KubernetesPartOfLabelKey:  constants.LLMInferenceServicePartOfValue,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     llmInferenceServiceSCCClusterRole,
		},
	}
}

func semanticSCCRoleBindingIsEqual(expected, curr *rbacv1.RoleBinding) bool {
	return equality.Semantic.DeepEqual(expected.Subjects, curr.Subjects) && semanticRoleBindingIsEqual(expected, curr)
}
