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
	"errors"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	netv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
	"github.com/kserve/kserve/pkg/utils"
)

// postReconcilePVCEvictors preserves terminal-only evictor failures, but a
// failed policy operation must still retry. Joining a terminal evictor error
// would suppress that retry through controller-runtime's errors.Is check.
func (r *LLMISVCReconciler) postReconcilePVCEvictors(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, evictorErr error) error {
	policyErr := r.reconcilePVCEvictorPlatformResources(ctx, llmSvc)
	if policyErr == nil {
		return evictorErr
	}
	if evictorErr == nil {
		return policyErr
	}
	// Retain both messages, but unwrap only the retryable policy error.
	return fmt.Errorf("%s; %w", evictorErr.Error(), policyErr)
}

func pvcEvictorNetworkPolicyName(deploymentName string) string {
	return kmeta.ChildName(deploymentName, "-networkpolicy-deny-all")
}

// expectedPVCEvictorNetworkPolicy uses the stored selector, never a guessed or
// namespace-wide selector. The Deployment owns the policy because a PVC/path
// evictor can be shared by several LLMInferenceServices.
func expectedPVCEvictorNetworkPolicy(deployment *appsv1.Deployment) (*netv1.NetworkPolicy, error) {
	key := client.ObjectKeyFromObject(deployment)
	claim := deployment.Annotations[pvcEvictorPVCName]
	path, hasPath := deployment.Annotations[pvcEvictorPVCPath]
	evictorKey := pvcEvictorKey(claim, path)
	if deployment.UID == "" || deployment.Labels[pvcEvictorLabel] != "true" ||
		claim == "" || !hasPath || path != normalizePVCEvictorPath(path) ||
		deployment.Labels[pvcEvictorKeyLabel] != evictorKey ||
		deployment.Spec.Template.Spec.ServiceAccountName != pvcEvictorServiceAccount {
		return nil, fmt.Errorf("Deployment %s has an invalid standalone PVC evictor identity", key)
	}
	selector, err := storedDeploymentPodSelector(deployment.Spec.Selector, key)
	if err != nil {
		return nil, err
	}
	// Require the existing identity constraints even if other stored selector
	// keys/expressions differ from the current template.
	if selector.MatchLabels[pvcEvictorLabel] != "true" || selector.MatchLabels[pvcEvictorKeyLabel] != evictorKey {
		return nil, fmt.Errorf("Deployment %s selector is not constrained to its PVC evictor identity", key)
	}
	parsed, err := metav1.LabelSelectorAsSelector(&selector)
	if err != nil {
		return nil, err
	}
	if !parsed.Matches(labels.Set(deployment.Spec.Template.Labels)) {
		return nil, fmt.Errorf("Deployment %s Pod template does not match its stored selector", key)
	}
	return &netv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:            pvcEvictorNetworkPolicyName(deployment.Name),
			Namespace:       deployment.Namespace,
			Labels:          map[string]string{pvcEvictorLabel: "true", pvcEvictorKeyLabel: evictorKey},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(deployment, appsv1.SchemeGroupVersion.WithKind("Deployment"))},
		},
		Spec: netv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []netv1.PolicyType{netv1.PolicyTypeIngress, netv1.PolicyTypeEgress},
			// No rules: isolate both directions. An empty rule would allow all.
		},
	}, nil
}

// reconcilePVCEvictorPlatformResources runs after shared-evictor reconciliation,
// including partial failures, and before routing/monitoring. It covers stored
// namespace evictors rather than only this service's desired targets. Deployment
// GC handles cleanup; service finalization must not prune shared policies.
func (r *LLMISVCReconciler) reconcilePVCEvictorPlatformResources(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	if networkPolicyDisabled {
		return nil
	}
	desired := make(map[string]struct{})
	if !utils.GetForceStopRuntime(llmSvc) {
		for _, target := range collectPVCEvictorTargets(llmSvc) {
			desired[pvcEvictorKey(target.claimName, target.subPath)] = struct{}{}
		}
	}
	markFailure := func(err error) {
		llmSvc.MarkPVCEvictorNotReady("ReconcilePVCEvictorNetworkPolicyError", "%s", err)
		llmSvc.DetermineWorkloadReadiness()
	}
	deployments := &appsv1.DeploymentList{}
	if err := r.List(ctx, deployments, client.InNamespace(llmSvc.Namespace), client.MatchingLabels{pvcEvictorLabel: "true"}); err != nil {
		err = fmt.Errorf("failed to list PVC evictor Deployments for NetworkPolicies: %w", err)
		if len(desired) > 0 {
			markFailure(err)
		}
		return err
	}
	var errs error
	for i := range deployments.Items {
		deployment := &deployments.Items[i]
		if !deployment.DeletionTimestamp.IsZero() {
			continue
		}
		expected, err := expectedPVCEvictorNetworkPolicy(deployment)
		if err == nil {
			err = Reconcile(ctx, r, deployment, &netv1.NetworkPolicy{}, expected, semanticNetworkPolicyIsEqual)
		}
		if err != nil {
			err = fmt.Errorf("failed to reconcile PVC evictor NetworkPolicy for %s: %w", client.ObjectKeyFromObject(deployment), err)
			errs = errors.Join(errs, err)
			_, byLabel := desired[deployment.Labels[pvcEvictorKeyLabel]]
			_, byIdentity := desired[pvcEvictorKey(deployment.Annotations[pvcEvictorPVCName], deployment.Annotations[pvcEvictorPVCPath])]
			if byLabel || byIdentity {
				markFailure(err)
			}
		}
	}
	return errs
}

// pvcEvictorNetworkPolicyPredicate also recognizes the deterministic name and
// Deployment owner, so removing managed labels still triggers reconciliation.
// Ownership tampering is reported as a conflict, not silently adopted.
func pvcEvictorNetworkPolicyPredicate() predicate.Predicate {
	isEvictorPolicy := func(object client.Object) bool {
		policy, ok := object.(*netv1.NetworkPolicy)
		if !ok || policy == nil {
			return false
		}
		if policy.Labels[pvcEvictorLabel] == "true" {
			return true
		}
		owner := metav1.GetControllerOf(policy)
		return owner != nil && owner.APIVersion == appsv1.SchemeGroupVersion.String() &&
			owner.Kind == "Deployment" && policy.Name == pvcEvictorNetworkPolicyName(owner.Name)
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return isEvictorPolicy(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return isEvictorPolicy(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return isEvictorPolicy(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if !isEvictorPolicy(e.ObjectOld) && !isEvictorPolicy(e.ObjectNew) {
				return false
			}
			oldPolicy, oldOK := e.ObjectOld.(*netv1.NetworkPolicy)
			newPolicy, newOK := e.ObjectNew.(*netv1.NetworkPolicy)
			if !oldOK || !newOK || oldPolicy == nil || newPolicy == nil {
				return true
			}
			return !equality.Semantic.DeepEqual(oldPolicy.Spec, newPolicy.Spec) ||
				!equality.Semantic.DeepEqual(oldPolicy.Labels, newPolicy.Labels) ||
				!equality.Semantic.DeepEqual(oldPolicy.Annotations, newPolicy.Annotations) ||
				!equality.Semantic.DeepEqual(oldPolicy.OwnerReferences, newPolicy.OwnerReferences) ||
				!equality.Semantic.DeepEqual(oldPolicy.DeletionTimestamp, newPolicy.DeletionTimestamp)
		},
	}
}
