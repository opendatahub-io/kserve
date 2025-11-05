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
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"

	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/sets"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/log"
	igwv1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha2"
)

// GVRInferencePoolV1Alpha2 is the GroupVersionResource for v1alpha2 InferencePool
var GVRInferencePoolV1Alpha2 = schema.GroupVersionResource{
	Group:    "inference.networking.x-k8s.io",
	Version:  "v1alpha2",
	Resource: "inferencepools",
}

// GVRInferenceModelV1Alpha2 is the GroupVersionResource for v1alpha2 InferenceModel
var GVRInferenceModelV1Alpha2 = schema.GroupVersionResource{
	Group:    "inference.networking.x-k8s.io",
	Version:  "v1alpha2",
	Resource: "inferencemodels",
}

func (r *LLMInferenceServiceReconciler) reconcileScheduler(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	log.FromContext(ctx).Info("Reconciling Scheduler")

	if err := r.reconcileSchedulerServiceAccount(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerInferenceModel(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerDeployment(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerService(ctx, llmSvc); err != nil {
		return err
	}
	if err := r.reconcileSchedulerInferencePool(ctx, llmSvc); err != nil {
		return err
	}
	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerAuthDelegatorBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, sa *corev1.ServiceAccount) error {
	authDelegatorBinding := r.expectedSchedulerAuthDelegatorBinding(llmSvc, sa)
	if !llmSvc.DeletionTimestamp.IsZero() || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, authDelegatorBinding)
	}

	if err := Reconcile(ctx, r, llmSvc, &rbacv1.ClusterRoleBinding{}, authDelegatorBinding, semanticClusterRoleBindingIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler clusterrolebinding %s: %w", authDelegatorBinding.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRole(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	role := r.expectedSchedulerRole(llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, role)
	}
	if err := Reconcile(ctx, r, llmSvc, &rbacv1.Role{}, role, semanticRoleIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler role %s/%s: %w", role.GetNamespace(), role.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRoleBinding(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, sa *corev1.ServiceAccount) error {
	roleBinding := r.expectedSchedulerRoleBinding(llmSvc, sa)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, roleBinding)
	}

	if err := Reconcile(ctx, r, llmSvc, &rbacv1.RoleBinding{}, roleBinding, semanticRoleBindingIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler rolebinding %s/%s: %w", roleBinding.GetNamespace(), roleBinding.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerServiceAccount(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	serviceAccount := r.expectedSchedulerServiceAccount(llmSvc)

	if !llmSvc.DeletionTimestamp.IsZero() {
		return r.reconcileSchedulerAuthDelegatorBinding(ctx, llmSvc, serviceAccount)
	}

	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, serviceAccount)
	}

	if err := Reconcile(ctx, r, llmSvc, &corev1.ServiceAccount{}, serviceAccount, semanticServiceAccountIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler service account %s/%s: %w", serviceAccount.GetNamespace(), serviceAccount.GetName(), err)
	}

	if err := r.reconcileSchedulerAuthDelegatorBinding(ctx, llmSvc, serviceAccount); err != nil {
		return err
	}

	if err := r.reconcileSchedulerRole(ctx, llmSvc); err != nil {
		return err
	}

	return r.reconcileSchedulerRoleBinding(ctx, llmSvc, serviceAccount)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerDeployment(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	scheduler := r.expectedSchedulerDeployment(ctx, llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, scheduler)
	}
	if err := Reconcile(ctx, r, llmSvc, &appsv1.Deployment{}, scheduler, semanticDeploymentIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler deployment %s/%s: %w", scheduler.GetNamespace(), scheduler.GetName(), err)
	}
	return r.propagateDeploymentStatus(ctx, scheduler, llmSvc.MarkSchedulerWorkloadReady, llmSvc.MarkSchedulerWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	// If router/scheduler disabled or BYO pool (HasRef), delete both variants and exit.
	expected := r.expectedSchedulerInferencePool(ctx, llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		if err := Delete(ctx, r, llmSvc, expected); err != nil { // v1 typed
			return err
		}
		return r.deleteAlpha2PoolIfExists(ctx, llmSvc) // best-effort alpha2
	}

	// 1) Ensure v1 InferencePool (typed) exists/updated.
	if err := Reconcile(ctx, r, llmSvc, &igwv1.InferencePool{}, expected, semanticInferencePoolIsEqual); err != nil {
		return err
	}

	// 2) Ensure v1alpha2 InferencePool (dynamic) exists/updated.
	if err := r.reconcileAlpha2Pool(ctx, llmSvc, expected); err != nil {
		return err
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerService(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	expected := r.expectedSchedulerService(ctx, llmSvc)
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, expected)
	}

	if err := Reconcile(ctx, r, llmSvc, &corev1.Service{}, expected, semanticServiceIsEqual); err != nil {
		return err
	}

	return nil
}

// reconcileSchedulerInferenceModel manages the v1alpha2 InferenceModel resource using the dynamic client.
// This follows the same Reconcile pattern as reconcileAlpha2Pool: Get -> Create if missing, or Update if different.
// The InferenceModel tells the scheduler which model to route requests for.
func (r *LLMInferenceServiceReconciler) reconcileSchedulerInferenceModel(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	// Clean up InferenceModel if scheduler is disabled
	if llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil {
		return r.deleteAlpha2InferenceModelIfExists(ctx, llmSvc)
	}

	expected := r.expectedAlpha2InferenceModel(llmSvc)
	res := r.DynamicClient.Resource(GVRInferenceModelV1Alpha2).Namespace(expected.GetNamespace())

	// Try to fetch the existing v1alpha2 InferenceModel
	curr, err := res.Get(ctx, expected.GetName(), metav1.GetOptions{})
	if err != nil {
		// If not found or CRD not installed, create it
		if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return fmt.Errorf("failed to get v1alpha2 InferenceModel %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
		}
		// Create new v1alpha2 InferenceModel
		if _, err := res.Create(ctx, expected, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create v1alpha2 InferenceModel %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
		}
		r.EventRecorder.Eventf(llmSvc, corev1.EventTypeNormal, "Created", "Created v1alpha2 InferenceModel %s/%s", expected.GetNamespace(), expected.GetName())
		return nil
	}

	// Verify this model is owned by our LLMInferenceService
	if !metav1.IsControlledBy(curr, llmSvc) {
		return fmt.Errorf("failed to update v1alpha2 InferenceModel %s/%s: it is not controlled by LLMInferenceService %s/%s",
			curr.GetNamespace(), curr.GetName(),
			llmSvc.Namespace, llmSvc.Name,
		)
	}

	// Copy resource version for update
	expected.SetResourceVersion(curr.GetResourceVersion())

	// Skip update if nothing has changed
	if semanticUnstructuredInferenceModelIsEqual(expected, curr) {
		return nil
	}

	// Update the v1alpha2 InferenceModel with new spec/labels/annotations
	if _, err := res.Update(ctx, expected, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update v1alpha2 InferenceModel %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}

	r.EventRecorder.Eventf(llmSvc, corev1.EventTypeNormal, "Updated", "Updated v1alpha2 InferenceModel %s/%s", expected.GetNamespace(), expected.GetName())
	return nil
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerService(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) *corev1.Service {
	logger := log.FromContext(ctx)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      llmSvc.Spec.Router.EPPServiceName(llmSvc),
			Namespace: llmSvc.GetNamespace(),
			Labels:    SchedulerLabels(llmSvc),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: SchedulerLabels(llmSvc),
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Template != nil {
		podSpec := llmSvc.Spec.Router.Scheduler.Template.DeepCopy()

		desiredPorts := sets.New("grpc", "grpc-health", "metrics")

		actualPorts := make(map[string]*corev1.ContainerPort)
		for _, container := range podSpec.Containers {
			for _, port := range container.Ports {
				if desiredPorts.Has(port.Name) {
					actualPorts[port.Name] = port.DeepCopy()
				}
			}
		}

		if len(desiredPorts) != len(actualPorts) {
			// TODO should this be raised as failing condition? + check if grpc port matches what's defined in the inferencepool
			logger.Info("some ports are not matching", "desired", desiredPorts, "actual", maps.Keys(actualPorts))
		}

		var servicePorts []corev1.ServicePort
		for name, port := range actualPorts {
			servicePorts = append(servicePorts, corev1.ServicePort{
				Name:       name,
				Port:       port.ContainerPort,
				TargetPort: intstr.FromString(name),
				Protocol:   port.Protocol,
			})
		}

		sort.Slice(servicePorts, func(i, j int) bool {
			return servicePorts[i].Name < servicePorts[j].Name
		})

		svc.Spec.Ports = servicePorts
	}

	log.FromContext(ctx).V(2).Info("Expected router EPP service", "service", svc)

	return svc
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) *igwv1.InferencePool {
	labels := SchedulerLabels(llmSvc)
	logger := log.FromContext(ctx)

	ip := &igwv1.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-inference-pool"),
			Namespace: llmSvc.GetNamespace(),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
		},
	}
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.Spec != nil {
		ip.Spec = *llmSvc.Spec.Router.Scheduler.Pool.Spec.DeepCopy()
	}

	// Ensure endpointPickerRef.port is set (required by GIE v1 API)
	// If not already set, default to scheduler gRPC port (9002)
	if ip.Spec.EndpointPickerRef.Port == nil {
		ip.Spec.EndpointPickerRef.Port = &igwv1.Port{
			Number: 9002,
		}
		logger.V(2).Info("Defaulting endpointPickerRef.port to 9002 for GIE v1 compatibility")
	}

	logger.V(2).Info("Expected router InferencePool", "inferencepool", ip)

	return ip
}

// Build v1alpha2 InferenceModel unstructured.
// NOTE: We avoid v1 typed IM (doesn't exist). We write the fields the scheduler expects.
func (r *LLMInferenceServiceReconciler) expectedAlpha2InferenceModel(llmSvc *v1alpha2.LLMInferenceService) *unstructured.Unstructured {
	name := kmeta.ChildName(llmSvc.Name, "-inference-model")
	group := "inference.networking.k8s.io" // pool group we target - updated to v1 group
	poolName := llmSvc.Spec.Router.Scheduler.InferencePoolName(llmSvc)

	// Default modelName to resource name if spec.model.name is empty.
	modelName := ptr.Deref(llmSvc.Spec.Model.Name, llmSvc.Name)

	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "inference.networking.x-k8s.io/v1alpha2",
			"kind":       "InferenceModel",
			"metadata": map[string]any{
				"name":      name,
				"namespace": llmSvc.Namespace,
				"labels":    SchedulerLabels(llmSvc),
				"ownerReferences": []any{
					map[string]any{
						"apiVersion": v1alpha2.LLMInferenceServiceGVK.GroupVersion().String(),
						"kind":       v1alpha2.LLMInferenceServiceGVK.Kind,
						"name":       llmSvc.Name,
						"uid":        string(llmSvc.UID),
						"controller": true,
					},
				},
			},
			"spec": map[string]any{
				"modelName": modelName,
				"poolRef": map[string]any{
					"group": group,
					"kind":  "InferencePool",
					"name":  poolName,
				},
				"criticality": "Critical",
			},
		},
	}
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerDeployment(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) *appsv1.Deployment {
	labels := SchedulerLabels(llmSvc)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-router-scheduler"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
			Labels: labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
			},
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Template != nil {
		d.Spec.Template.Spec = *llmSvc.Spec.Router.Scheduler.Template.DeepCopy()
		for i := range d.Spec.Template.Spec.Containers {
			if d.Spec.Template.Spec.Containers[i].Name != "main" {
				continue
			}

			if slices.Contains(d.Spec.Template.Spec.Containers[i].Args, "--config-text") ||
				slices.Contains(d.Spec.Template.Spec.Containers[i].Args, "-config-text") ||
				slices.Contains(d.Spec.Template.Spec.Containers[i].Args, "--config-file") ||
				slices.Contains(d.Spec.Template.Spec.Containers[i].Args, "-config-file") {
				// When the configuration is overridden, don't add/override it.
				break
			}

			d.Spec.Template.Spec.Containers[i].Args = append(d.Spec.Template.Spec.Containers[i].Args,
				"--config-text",
				schedulerConfigText(llmSvc),
			)
		}
	}

	log.FromContext(ctx).V(2).Info("Expected router scheduler deployment", "deployment", d)

	return d
}

func schedulerConfigText(llmSvc *v1alpha2.LLMInferenceService) string {
	switch {
	case llmSvc.Spec.Prefill != nil:
		// Always do P/D by default (threshold 0)
		return `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
  - type: prefill-header-handler
  - type: prefill-filter
  - type: decode-filter
  - type: max-score-picker
  - type: prefix-cache-scorer
  - type: queue-scorer
  - type: pd-profile-handler
    parameters:
      threshold: 0
schedulingProfiles:
  - name: prefill
    plugins:
      - pluginRef: prefill-filter
      - pluginRef: queue-scorer
        weight: 1.0
      - pluginRef: max-score-picker
  - name: decode
    plugins:
      - pluginRef: decode-filter
      - pluginRef: queue-scorer
        weight: 1.0
      - pluginRef: max-score-picker
`
	default:
		return `
apiVersion: inference.networking.x-k8s.io/v1alpha1
kind: EndpointPickerConfig
plugins:
- type: single-profile-handler
- type: prefix-cache-scorer
- type: load-aware-scorer
- type: max-score-picker
schedulingProfiles:
- name: default
  plugins:
  - pluginRef: prefix-cache-scorer
    weight: 2.0
  - pluginRef: load-aware-scorer
    weight: 1.0
  - pluginRef: max-score-picker
`
	}
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerServiceAccount(llmSvc *v1alpha2.LLMInferenceService) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-sa"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
			Labels: SchedulerLabels(llmSvc),
		},
	}

	if llmSvc.Spec.Router != nil &&
		llmSvc.Spec.Router.Scheduler != nil &&
		llmSvc.Spec.Router.Scheduler.Template != nil &&
		llmSvc.Spec.Router.Scheduler.Template.ServiceAccountName != "" {
		sa.Name = llmSvc.Spec.Router.Scheduler.Template.ServiceAccountName
	}

	return sa
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerAuthDelegatorBinding(llmSvc *v1alpha2.LLMInferenceService, sa *corev1.ServiceAccount) *rbacv1.ClusterRoleBinding {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:   kmeta.ChildName(llmSvc.GetNamespace(), "-"+llmSvc.GetName()+"-epp-auth-rb"),
			Labels: SchedulerLabels(llmSvc),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.GetName(),
			Namespace: sa.GetNamespace(),
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	return crb
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerRole(llmSvc *v1alpha2.LLMInferenceService) *rbacv1.Role {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-role"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
			Labels: SchedulerLabels(llmSvc),
		},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"inference.networking.x-k8s.io"}, Resources: []string{"inferencepools", "inferencemodels", "inferenceobjectives"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"inference.networking.k8s.io"}, Resources: []string{"inferencepools", "inferencemodels"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	return role
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerRoleBinding(llmSvc *v1alpha2.LLMInferenceService, sa *corev1.ServiceAccount) *rbacv1.RoleBinding {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-rb"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha2.LLMInferenceServiceGVK),
			},
			Labels: SchedulerLabels(llmSvc),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      sa.GetName(),
			Namespace: sa.GetNamespace(),
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     kmeta.ChildName(llmSvc.GetName(), "-epp-role"),
		},
	}
	return rb
}

func semanticServiceIsEqual(expected *corev1.Service, current *corev1.Service) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}

func semanticInferencePoolIsEqual(expected *igwv1.InferencePool, curr *igwv1.InferencePool) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, curr.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}

func semanticUnstructuredInferencePoolIsEqual(expected, curr *unstructured.Unstructured) bool {
	expectedSpec, _, _ := unstructured.NestedMap(expected.Object, "spec")
	currSpec, _, _ := unstructured.NestedMap(curr.Object, "spec")
	return equality.Semantic.DeepDerivative(expectedSpec, currSpec) &&
		equality.Semantic.DeepDerivative(expected.GetLabels(), curr.GetLabels()) &&
		equality.Semantic.DeepDerivative(expected.GetAnnotations(), curr.GetAnnotations())
}

func semanticUnstructuredInferenceModelIsEqual(expected, curr *unstructured.Unstructured) bool {
	expectedSpec, _, _ := unstructured.NestedMap(expected.Object, "spec")
	currSpec, _, _ := unstructured.NestedMap(curr.Object, "spec")
	return equality.Semantic.DeepDerivative(expectedSpec, currSpec) &&
		equality.Semantic.DeepDerivative(expected.GetLabels(), curr.GetLabels()) &&
		equality.Semantic.DeepDerivative(expected.GetAnnotations(), curr.GetAnnotations())
}

func semanticServiceAccountIsEqual(expected *corev1.ServiceAccount, current *corev1.ServiceAccount) bool {
	return equality.Semantic.DeepDerivative(expected.Secrets, current.Secrets) &&
		equality.Semantic.DeepDerivative(expected.ImagePullSecrets, current.ImagePullSecrets) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}

func semanticRoleIsEqual(expected *rbacv1.Role, curr *rbacv1.Role) bool {
	return equality.Semantic.DeepDerivative(expected.Rules, curr.Rules) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}

func semanticClusterRoleBindingIsEqual(expected *rbacv1.ClusterRoleBinding, curr *rbacv1.ClusterRoleBinding) bool {
	return equality.Semantic.DeepDerivative(expected.Subjects, curr.Subjects) &&
		equality.Semantic.DeepDerivative(expected.RoleRef, curr.RoleRef) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}

func semanticRoleBindingIsEqual(expected *rbacv1.RoleBinding, curr *rbacv1.RoleBinding) bool {
	return equality.Semantic.DeepDerivative(expected.Subjects, curr.Subjects) &&
		equality.Semantic.DeepDerivative(expected.RoleRef, curr.RoleRef) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
}

func SchedulerLabels(llmSvc *v1alpha2.LLMInferenceService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}
}

// consider pool "Ready" if any Parent has Accepted=True AND ResolvedRefs=True
func isV1PoolReady(p *igwv1.InferencePool) bool {
	for _, ps := range p.Status.Parents {
		accepted, resolved := false, false
		for _, c := range ps.Conditions {
			// c.Type is string, c.Status is ConditionStatus (string alias) - no conversion needed
			if c.Type == "Accepted" && c.Status == "True" {
				accepted = true
			}
			if c.Type == "ResolvedRefs" && c.Status == "True" {
				resolved = true
			}
		}
		if accepted && resolved {
			return true
		}
	}
	return false
}

// alpha2 check via dynamic client
func (r *LLMInferenceServiceReconciler) isAlpha2PoolReady(ctx context.Context, ns, name string) bool {
	u, err := r.DynamicClient.Resource(GVRInferencePoolV1Alpha2).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false
	}
	parent, _, _ := unstructured.NestedSlice(u.Object, "status", "parent")
	for _, p := range parent {
		pm, _ := p.(map[string]any)
		conds, _, _ := unstructured.NestedSlice(pm, "conditions")
		accepted, resolved := false, false
		for _, cc := range conds {
			cm, _ := cc.(map[string]any)
			if cm["type"] == "Accepted" && cm["status"] == "True" {
				accepted = true
			}
			if cm["type"] == "ResolvedRefs" && cm["status"] == "True" {
				resolved = true
			}
		}
		if accepted && resolved {
			return true
		}
	}
	return false
}

// reconcileAlpha2Pool manages the v1alpha2 InferencePool resource using the dynamic client.
// This follows the standard Reconcile pattern: Get -> Create if missing, or Update if different.
// It ensures ownership and only updates when there are actual changes (semantic equality check).
func (r *LLMInferenceServiceReconciler) reconcileAlpha2Pool(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService, v1pool *igwv1.InferencePool) error {
	// Convert v1 typed pool to v1alpha2 unstructured format
	expected, err := v1ToAlpha2Unstructured(v1pool)
	if err != nil {
		return err
	}

	res := r.DynamicClient.Resource(GVRInferencePoolV1Alpha2).Namespace(expected.GetNamespace())

	// Try to fetch the existing v1alpha2 InferencePool
	curr, err := res.Get(ctx, expected.GetName(), metav1.GetOptions{})
	if err != nil {
		// If not found or CRD not installed, create it
		if !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return fmt.Errorf("failed to get v1alpha2 InferencePool %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
		}
		// Create new v1alpha2 InferencePool
		if _, err := res.Create(ctx, expected, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("failed to create v1alpha2 InferencePool %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
		}
		r.EventRecorder.Eventf(llmSvc, corev1.EventTypeNormal, "Created", "Created v1alpha2 InferencePool %s/%s", expected.GetNamespace(), expected.GetName())
		return nil
	}

	// Verify this pool is owned by our LLMInferenceService (prevents accidental overwrites)
	if !metav1.IsControlledBy(curr, llmSvc) {
		return fmt.Errorf("failed to update v1alpha2 InferencePool %s/%s: it is not controlled by LLMInferenceService %s/%s",
			curr.GetNamespace(), curr.GetName(),
			llmSvc.Namespace, llmSvc.Name,
		)
	}

	// Copy resource version so we can update the existing object
	expected.SetResourceVersion(curr.GetResourceVersion())

	// Skip update if nothing has changed (avoids unnecessary API calls and reconciles)
	if semanticUnstructuredInferencePoolIsEqual(expected, curr) {
		return nil
	}

	// Update the v1alpha2 InferencePool with new spec/labels/annotations
	if _, err := res.Update(ctx, expected, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update v1alpha2 InferencePool %s/%s: %w", expected.GetNamespace(), expected.GetName(), err)
	}

	r.EventRecorder.Eventf(llmSvc, corev1.EventTypeNormal, "Updated", "Updated v1alpha2 InferencePool %s/%s", expected.GetNamespace(), expected.GetName())
	return nil
}

func (r *LLMInferenceServiceReconciler) deleteAlpha2PoolIfExists(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	name := kmeta.ChildName(llmSvc.Name, "-inference-pool")
	res := r.DynamicClient.Resource(GVRInferencePoolV1Alpha2).Namespace(llmSvc.Namespace)
	_, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// If resource doesn't exist (NotFound), that's fine - nothing to delete
		if apierrors.IsNotFound(err) {
			return nil
		}
		// For other errors, propagate them
		return err
	}
	return res.Delete(ctx, name, metav1.DeleteOptions{})
}

func (r *LLMInferenceServiceReconciler) deleteAlpha2InferenceModelIfExists(ctx context.Context, llmSvc *v1alpha2.LLMInferenceService) error {
	name := kmeta.ChildName(llmSvc.Name, "-inference-model")
	res := r.DynamicClient.Resource(GVRInferenceModelV1Alpha2).Namespace(llmSvc.Namespace)
	_, err := res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// If resource doesn't exist (NotFound) or CRD not installed, that's fine
		if apierrors.IsNotFound(err) {
			return nil
		}
		// For other errors, propagate them
		return err
	}
	return res.Delete(ctx, name, metav1.DeleteOptions{})
}

// Convert the typed v1 pool to a v1alpha2 unstructured object.
// NOTE: v1 uses typed LabelKey/LabelValue and non-pointer Kind/Number/FailureMode.
// We convert keys/values to strings and use "" / >0 checks instead of nil checks.
func v1ToAlpha2Unstructured(v1p *igwv1.InferencePool) (*unstructured.Unstructured, error) {
	if v1p == nil {
		return nil, errors.New("nil v1 pool")
	}

	// selector: v1 -> v1alpha2 (string map)
	selector := map[string]any{}
	if v1p.Spec.Selector.MatchLabels != nil {
		for k, v := range v1p.Spec.Selector.MatchLabels {
			selector[string(k)] = string(v) // v1 uses typed keys/values; alpha2 wants plain strings
		}
	}

	// target port: v1 TargetPorts[0].Number -> alpha2 targetPortNumber (int64)
	if len(v1p.Spec.TargetPorts) == 0 {
		return nil, errors.New("spec.targetPorts[0] required")
	}
	tp := int64(v1p.Spec.TargetPorts[0].Number) // Number is a non-pointer alias (int32)

	// endpointPickerRef -> extensionRef
	// IMPORTANT: Kind/Group/FailureMode are value types in v1, not pointers.
	ext := map[string]any{
		"name": string(v1p.Spec.EndpointPickerRef.Name),
	}
	if v1p.Spec.EndpointPickerRef.Group != nil && *v1p.Spec.EndpointPickerRef.Group != "" {
		ext["group"] = string(*v1p.Spec.EndpointPickerRef.Group) // ✅ deref the *Group
	}
	if s := string(v1p.Spec.EndpointPickerRef.Kind); s != "" {
		ext["kind"] = s
	}
	if v1p.Spec.EndpointPickerRef.Port != nil && v1p.Spec.EndpointPickerRef.Port.Number > 0 {
		ext["portNumber"] = int64(v1p.Spec.EndpointPickerRef.Port.Number)
	}
	if s := string(v1p.Spec.EndpointPickerRef.FailureMode); s != "" {
		ext["failureMode"] = s
	}

	metadata := map[string]any{
		"name":      v1p.ObjectMeta.Name,
		"namespace": v1p.ObjectMeta.Namespace,
	}
	if v1p.ObjectMeta.Labels != nil {
		metadata["labels"] = v1p.ObjectMeta.Labels
	}
	if v1p.ObjectMeta.Annotations != nil {
		metadata["annotations"] = v1p.ObjectMeta.Annotations
	}

	// Convert ownerReferences to unstructured format
	if len(v1p.ObjectMeta.OwnerReferences) > 0 {
		ownerRefs := make([]any, len(v1p.ObjectMeta.OwnerReferences))
		for i, ref := range v1p.ObjectMeta.OwnerReferences {
			ownerRef := map[string]any{
				"apiVersion": ref.APIVersion,
				"kind":       ref.Kind,
				"name":       ref.Name,
				"uid":        string(ref.UID),
			}
			if ref.Controller != nil {
				ownerRef["controller"] = *ref.Controller
			}
			if ref.BlockOwnerDeletion != nil {
				ownerRef["blockOwnerDeletion"] = *ref.BlockOwnerDeletion
			}
			ownerRefs[i] = ownerRef
		}
		metadata["ownerReferences"] = ownerRefs
	}

	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "inference.networking.x-k8s.io/v1alpha2",
			"kind":       "InferencePool",
			"metadata":   metadata,
			"spec": map[string]any{
				"selector":         selector,
				"targetPortNumber": tp,
				"extensionRef":     ext,
			},
		},
	}
	return u, nil
}
