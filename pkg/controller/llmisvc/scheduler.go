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
	"fmt"
	"maps"
	"slices"
	"sort"

	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/util/sets"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"knative.dev/pkg/kmeta"
	"sigs.k8s.io/controller-runtime/pkg/log"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1alpha2"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/utils"
)

// inferencePoolV1GVR is the GroupVersionResource for v1 InferencePool (inference.networking.k8s.io)
var inferencePoolV1GVR = schema.GroupVersionResource{
	Group:    constants.InferencePoolV1Group,
	Version:  "v1",
	Resource: "inferencepools",
}

func (r *LLMInferenceServiceReconciler) reconcileScheduler(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
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

	if utils.GetForceStopRuntime(llmSvc) {
		llmSvc.MarkInferencePoolNotReady("Stopped", "Service is stopped")
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerAuthDelegatorBinding(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) error {
	authDelegatorBinding := r.expectedSchedulerAuthDelegatorBinding(llmSvc, sa)
	if utils.GetForceStopRuntime(llmSvc) || !llmSvc.DeletionTimestamp.IsZero() || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, authDelegatorBinding)
	}

	if err := Reconcile(ctx, r, llmSvc, &rbacv1.ClusterRoleBinding{}, authDelegatorBinding, semanticClusterRoleBindingIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler clusterrolebinding %s: %w", authDelegatorBinding.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRole(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	role := r.expectedSchedulerRole(llmSvc)
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, role)
	}
	if err := Reconcile(ctx, r, llmSvc, &rbacv1.Role{}, role, semanticRoleIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler role %s/%s: %w", role.GetNamespace(), role.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerRoleBinding(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) error {
	roleBinding := r.expectedSchedulerRoleBinding(llmSvc, sa)
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, roleBinding)
	}

	if err := Reconcile(ctx, r, llmSvc, &rbacv1.RoleBinding{}, roleBinding, semanticRoleBindingIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler rolebinding %s/%s: %w", roleBinding.GetNamespace(), roleBinding.GetName(), err)
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerServiceAccount(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	serviceAccount := r.expectedSchedulerServiceAccount(llmSvc)

	if !llmSvc.DeletionTimestamp.IsZero() {
		return r.reconcileSchedulerAuthDelegatorBinding(ctx, llmSvc, serviceAccount)
	}

	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
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

func (r *LLMInferenceServiceReconciler) reconcileSchedulerDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	scheduler := r.expectedSchedulerDeployment(ctx, llmSvc)
	if isStopped := utils.GetForceStopRuntime(llmSvc); isStopped || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		if isStopped {
			llmSvc.MarkSchedulerWorkloadNotReady("Stopped", "Service is stopped")
		} else {
			llmSvc.MarkSchedulerWorkloadUnset()
		}
		return Delete(ctx, r, llmSvc, scheduler)
	}
	if err := Reconcile(ctx, r, llmSvc, &appsv1.Deployment{}, scheduler, semanticDeploymentIsEqual); err != nil {
		return fmt.Errorf("failed to reconcile scheduler deployment %s/%s: %w", scheduler.GetNamespace(), scheduler.GetName(), err)
	}
	return r.propagateDeploymentStatus(ctx, scheduler, llmSvc.MarkSchedulerWorkloadReady, llmSvc.MarkSchedulerWorkloadNotReady)
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	logger := log.FromContext(ctx)
	expected := r.expectedSchedulerInferencePool(ctx, llmSvc)

	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		// Delete both v1alpha2 and v1 pools
		if err := Delete(ctx, r, llmSvc, expected); err != nil {
			return err
		}
		return r.deleteV1InferencePool(ctx, llmSvc, expected)
	}

	// Reconcile v1alpha2 InferencePool (typed client)
	if err := Reconcile(ctx, r, llmSvc, &igwapi.InferencePool{}, expected, semanticInferencePoolIsEqual); err != nil {
		return err
	}

	// Also reconcile v1 InferencePool (dynamic/unstructured client) for Gateway compatibility
	// Some Gateways (e.g., Istio 1.28+) only support v1 InferencePool
	if err := r.reconcileV1InferencePool(ctx, llmSvc, expected); err != nil {
		logger.Error(err, "Failed to reconcile v1 InferencePool, continuing with v1alpha2 only")
		// Don't fail reconciliation - v1alpha2 might still work depending on Gateway
	}

	// TODO add inference pool condition propagation and then aggregate it into "RouterReady" similar to WorkloadReady.
	return nil
}

// reconcileV1InferencePool creates/updates a v1 InferencePool using the dynamic client.
// This is needed because some Gateways (e.g., Istio 1.28+) only support the v1 API
// (inference.networking.k8s.io) and not v1alpha2 (inference.networking.x-k8s.io).
// This function follows the same pattern as the generic Reconcile function in lifecycle_crud.go.
func (r *LLMInferenceServiceReconciler) reconcileV1InferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, v1alpha2Pool *igwapi.InferencePool) error {
	logger := log.FromContext(ctx)

	if r.DynamicClient == nil {
		// DynamicClient should always be configured; panic during tests to catch misconfiguration
		panic("DynamicClient is nil - controller is misconfigured")
	}

	// Build unstructured v1 InferencePool from the v1alpha2 pool
	expected := expectedSchedulerInferencePoolV1(v1alpha2Pool)

	logger.V(1).Info("Reconciling v1 InferencePool", "name", expected.GetName(), "namespace", expected.GetNamespace())

	// Get current v1 InferencePool
	curr, err := r.DynamicClient.Resource(inferencePoolV1GVR).Namespace(expected.GetNamespace()).Get(ctx, expected.GetName(), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Create new v1 InferencePool
			_, err = r.DynamicClient.Resource(inferencePoolV1GVR).Namespace(expected.GetNamespace()).Create(ctx, expected, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("failed to create v1 InferencePool: %w", err)
			}
			logger.Info("Created v1 InferencePool", "name", expected.GetName(), "namespace", expected.GetNamespace())
			r.Eventf(llmSvc, corev1.EventTypeNormal, "Created", "Created v1 InferencePool %s/%s", expected.GetNamespace(), expected.GetName())
			return nil
		}
		return fmt.Errorf("failed to get v1 InferencePool: %w", err)
	}

	// Check ownership - ensure the current resource is controlled by the same owner
	if !isControlledByOwner(curr, v1alpha2Pool.OwnerReferences) {
		return fmt.Errorf("v1 InferencePool %s/%s is not controlled by LLMInferenceService %s/%s",
			curr.GetNamespace(), curr.GetName(), llmSvc.GetNamespace(), llmSvc.GetName())
	}

	// Compare spec, labels, and annotations to determine if update is needed
	if semanticUnstructuredInferencePoolIsEqual(expected, curr) {
		logger.V(2).Info("v1 InferencePool is up to date, skipping update", "name", expected.GetName())
		return nil
	}

	// Set resourceVersion for update
	expected.SetResourceVersion(curr.GetResourceVersion())

	// Update existing v1 InferencePool
	_, err = r.DynamicClient.Resource(inferencePoolV1GVR).Namespace(expected.GetNamespace()).Update(ctx, expected, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update v1 InferencePool: %w", err)
	}
	logger.Info("Updated v1 InferencePool", "name", expected.GetName(), "namespace", expected.GetNamespace())
	r.Eventf(llmSvc, corev1.EventTypeNormal, "Updated", "Updated v1 InferencePool %s/%s", expected.GetNamespace(), expected.GetName())

	return nil
}

// expectedSchedulerInferencePoolV1 creates an unstructured v1 InferencePool from a v1alpha2 pool.
func expectedSchedulerInferencePoolV1(v1alpha2Pool *igwapi.InferencePool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   constants.InferencePoolV1Group,
		Version: "v1",
		Kind:    "InferencePool",
	})
	u.SetName(v1alpha2Pool.Name)
	u.SetNamespace(v1alpha2Pool.Namespace)
	u.SetLabels(v1alpha2Pool.Labels)
	u.SetAnnotations(v1alpha2Pool.Annotations)
	u.SetOwnerReferences(v1alpha2Pool.OwnerReferences)

	// Build spec - convert from v1alpha2 to v1 API fields
	// v1alpha2 -> v1 field mapping:
	//   targetPortNumber (int32) -> targetPorts ([]TargetPort)
	//   selector (map) -> selector.matchLabels (map)
	//   extensionRef -> endpointPickerRef
	spec := map[string]interface{}{
		// v1 uses targetPorts array with "number" field instead of single targetPortNumber
		// Cast int32 to int64 for JSON compatibility in unstructured objects
		"targetPorts": []interface{}{
			map[string]interface{}{
				"number": int64(v1alpha2Pool.Spec.TargetPortNumber),
			},
		},
	}

	// Convert selector (v1alpha2: flat map -> v1: selector.matchLabels)
	if v1alpha2Pool.Spec.Selector != nil {
		matchLabels := make(map[string]interface{})
		for k, v := range v1alpha2Pool.Spec.Selector {
			matchLabels[string(k)] = string(v)
		}
		spec["selector"] = map[string]interface{}{
			"matchLabels": matchLabels,
		}
	}

	// Convert extensionRef to endpointPickerRef (v1alpha2: extensionRef -> v1: endpointPickerRef)
	// v1 requires a port.number field when kind is Service, EPP service uses gRPC port 9002
	if v1alpha2Pool.Spec.ExtensionRef != nil && v1alpha2Pool.Spec.ExtensionRef.Name != "" {
		endpointPickerRef := map[string]interface{}{
			"name": string(v1alpha2Pool.Spec.ExtensionRef.Name),
			"port": map[string]interface{}{
				// Cast int32 to int64 for JSON compatibility in unstructured objects
				"number": int64(v1alpha2Pool.Spec.TargetPortNumber),
			},
		}
		if v1alpha2Pool.Spec.ExtensionRef.Group != nil {
			endpointPickerRef["group"] = string(*v1alpha2Pool.Spec.ExtensionRef.Group)
		}
		if v1alpha2Pool.Spec.ExtensionRef.Kind != nil {
			endpointPickerRef["kind"] = string(*v1alpha2Pool.Spec.ExtensionRef.Kind)
		}
		spec["endpointPickerRef"] = endpointPickerRef
	}

	u.Object["spec"] = spec
	return u
}

// semanticUnstructuredInferencePoolIsEqual compares two unstructured InferencePool objects.
func semanticUnstructuredInferencePoolIsEqual(expected, curr *unstructured.Unstructured) bool {
	// Compare spec
	expectedSpec, _, _ := unstructured.NestedMap(expected.Object, "spec")
	currSpec, _, _ := unstructured.NestedMap(curr.Object, "spec")
	if !equality.Semantic.DeepDerivative(expectedSpec, currSpec) {
		return false
	}

	// Compare labels
	if !equality.Semantic.DeepDerivative(expected.GetLabels(), curr.GetLabels()) {
		return false
	}

	// Compare annotations
	if !equality.Semantic.DeepDerivative(expected.GetAnnotations(), curr.GetAnnotations()) {
		return false
	}

	return true
}

// isControlledByOwner checks if the unstructured object is controlled by one of the given owner references.
func isControlledByOwner(obj *unstructured.Unstructured, expectedOwners []metav1.OwnerReference) bool {
	if len(expectedOwners) == 0 {
		return true
	}

	objOwners := obj.GetOwnerReferences()
	for _, expected := range expectedOwners {
		if expected.Controller != nil && *expected.Controller {
			for _, actual := range objOwners {
				if actual.Controller != nil && *actual.Controller &&
					actual.UID == expected.UID {
					return true
				}
			}
		}
	}
	return false
}

// deleteV1InferencePool deletes the v1 InferencePool using the dynamic client.
// This function follows the same pattern as the generic Delete function in lifecycle_crud.go.
func (r *LLMInferenceServiceReconciler) deleteV1InferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService, v1alpha2Pool *igwapi.InferencePool) error {
	logger := log.FromContext(ctx)

	if r.DynamicClient == nil {
		panic("DynamicClient is nil - controller is misconfigured")
	}

	// Get the existing resource first
	existing, err := r.DynamicClient.Resource(inferencePoolV1GVR).Namespace(v1alpha2Pool.Namespace).Get(ctx, v1alpha2Pool.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to get v1 InferencePool: %w", err)
	}

	// Check if already being deleted
	if existing.GetDeletionTimestamp() != nil && !existing.GetDeletionTimestamp().IsZero() {
		return nil
	}

	// Check ownership before deleting
	if !isControlledByOwner(existing, v1alpha2Pool.OwnerReferences) {
		return fmt.Errorf("cannot delete v1 InferencePool %s/%s: not owned by LLMInferenceService %s/%s",
			existing.GetNamespace(), existing.GetName(), llmSvc.GetNamespace(), llmSvc.GetName())
	}

	// If owner is being deleted, let GC handle it
	if llmSvc != nil && !llmSvc.GetDeletionTimestamp().IsZero() {
		return nil
	}

	err = r.DynamicClient.Resource(inferencePoolV1GVR).Namespace(v1alpha2Pool.Namespace).Delete(ctx, v1alpha2Pool.Name, metav1.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete v1 InferencePool: %w", err)
	}
	logger.Info("Deleted v1 InferencePool", "name", v1alpha2Pool.Name, "namespace", v1alpha2Pool.Namespace)
	r.Eventf(llmSvc, corev1.EventTypeNormal, "Deleted", "Deleted v1 InferencePool %s/%s", v1alpha2Pool.Namespace, v1alpha2Pool.Name)

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerService(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedSchedulerService(ctx, llmSvc)
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, expected)
	}

	if err := Reconcile(ctx, r, llmSvc, &corev1.Service{}, expected, semanticServiceIsEqual); err != nil {
		return err
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) reconcileSchedulerInferenceModel(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) error {
	expected := r.expectedSchedulerInferenceModel(ctx, llmSvc)
	if utils.GetForceStopRuntime(llmSvc) || llmSvc.Spec.Router == nil || llmSvc.Spec.Router.Scheduler == nil || llmSvc.Spec.Router.Scheduler.Template == nil || llmSvc.Spec.Router.Scheduler.Pool.HasRef() {
		return Delete(ctx, r, llmSvc, expected)
	}

	if err := Reconcile(ctx, r, llmSvc, &igwapi.InferenceModel{}, expected, semanticInferenceModelIsEqual); err != nil {
		return err
	}

	return nil
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerService(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *corev1.Service {
	logger := log.FromContext(ctx)
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      llmSvc.Spec.Router.EPPServiceName(llmSvc),
			Namespace: llmSvc.GetNamespace(),
			Labels:    SchedulerLabels(llmSvc),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: SchedulerLabels(llmSvc),
		},
	}

	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Template != nil {
		podSpec := llmSvc.Spec.Router.Scheduler.Template.DeepCopy()

		desiredPorts := sets.New("grpc", "grpc-health", "metrics", "zmq")

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

func (r *LLMInferenceServiceReconciler) expectedSchedulerInferencePool(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *igwapi.InferencePool {
	labels := SchedulerLabels(llmSvc)

	ip := &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-inference-pool"),
			Namespace: llmSvc.GetNamespace(),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
		},
	}
	if llmSvc.Spec.Router != nil && llmSvc.Spec.Router.Scheduler != nil && llmSvc.Spec.Router.Scheduler.Pool != nil && llmSvc.Spec.Router.Scheduler.Pool.Spec != nil {
		ip.Spec = *llmSvc.Spec.Router.Scheduler.Pool.Spec.DeepCopy()
	}

	log.FromContext(ctx).V(2).Info("Expected router InferencePool", "inferencepool", ip)

	return ip
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerInferenceModel(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *igwapi.InferenceModel {
	labels := SchedulerLabels(llmSvc)

	im := &igwapi.InferenceModel{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-inference-model"),
			Namespace: llmSvc.GetNamespace(),
			Labels:    labels,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
		},
		Spec: igwapi.InferenceModelSpec{
			ModelName: ptr.Deref(llmSvc.Spec.Model.Name, llmSvc.GetName()),
			PoolRef: igwapi.PoolObjectReference{
				Group: "inference.networking.x-k8s.io",
				Kind:  "InferencePool",
				Name:  igwapi.ObjectName(kmeta.ChildName(llmSvc.GetName(), "-inference-pool")),
			},
			Criticality: llmSvc.Spec.Model.Criticality,
		},
	}
	if im.Spec.Criticality == nil {
		im.Spec.Criticality = ptr.To(igwapi.Critical)
	}

	log.FromContext(ctx).V(2).Info("Expected InferenceModel", "inferencemodel", im)

	return im
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerDeployment(ctx context.Context, llmSvc *v1alpha1.LLMInferenceService) *appsv1.Deployment {
	labels := SchedulerLabels(llmSvc)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-kserve-router-scheduler"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
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

func schedulerConfigText(llmSvc *v1alpha1.LLMInferenceService) string {
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
      - pluginRef: prefix-cache-scorer
        weight: 1.0
      - pluginRef: max-score-picker
  - name: decode
    plugins:
      - pluginRef: decode-filter
      - pluginRef: queue-scorer
        weight: 1.0
      - pluginRef: prefix-cache-scorer
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

func (r *LLMInferenceServiceReconciler) expectedSchedulerServiceAccount(llmSvc *v1alpha1.LLMInferenceService) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-sa"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
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

func (r *LLMInferenceServiceReconciler) expectedSchedulerAuthDelegatorBinding(llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) *rbacv1.ClusterRoleBinding {
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

func (r *LLMInferenceServiceReconciler) expectedSchedulerRole(llmSvc *v1alpha1.LLMInferenceService) *rbacv1.Role {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-role"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
			},
			Labels: SchedulerLabels(llmSvc),
		},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"inference.networking.x-k8s.io"}, Resources: []string{"inferencepools", "inferenceobjectives"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"inference.networking.k8s.io"}, Resources: []string{"inferencepools"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
	return role
}

func (r *LLMInferenceServiceReconciler) expectedSchedulerRoleBinding(llmSvc *v1alpha1.LLMInferenceService, sa *corev1.ServiceAccount) *rbacv1.RoleBinding {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kmeta.ChildName(llmSvc.GetName(), "-epp-rb"),
			Namespace: llmSvc.GetNamespace(),
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(llmSvc, v1alpha1.LLMInferenceServiceGVK),
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

func semanticInferenceModelIsEqual(expected *igwapi.InferenceModel, current *igwapi.InferenceModel) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, current.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, current.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, current.Annotations)
}

func semanticInferencePoolIsEqual(expected *igwapi.InferencePool, curr *igwapi.InferencePool) bool {
	return equality.Semantic.DeepDerivative(expected.Spec, curr.Spec) &&
		equality.Semantic.DeepDerivative(expected.Labels, curr.Labels) &&
		equality.Semantic.DeepDerivative(expected.Annotations, curr.Annotations)
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

func SchedulerLabels(llmSvc *v1alpha1.LLMInferenceService) map[string]string {
	return map[string]string{
		"app.kubernetes.io/component": "llminferenceservice-router-scheduler",
		"app.kubernetes.io/name":      llmSvc.GetName(),
		"app.kubernetes.io/part-of":   "llminferenceservice",
	}
}
