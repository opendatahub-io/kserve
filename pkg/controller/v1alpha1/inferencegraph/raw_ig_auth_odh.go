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

package inferencegraph

import (
	"context"

	"github.com/pkg/errors"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	corev1cfg "k8s.io/client-go/applyconfigurations/core/v1"
	metav1cfg "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacv1cfg "k8s.io/client-go/applyconfigurations/rbac/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// InferenceGraphControllerName is the field manager for the auth-verifier ServiceAccount and
// the kserve-inferencegraph-auth-verifiers ClusterRoleBinding.
const InferenceGraphControllerName string = "inferencegraph-controller"

func handleInferenceGraphRawAuthResources(ctx context.Context, clientset kubernetes.Interface, scheme *runtime.Scheme, graph *v1alpha1.InferenceGraph) error {
	saName := getServiceAccountNameForGraph(graph)

	if graph.GetAnnotations()[constants.ODHKserveRawAuth] == "true" {
		graphGVK, err := apiutil.GVKForObject(graph, scheme)
		if err != nil {
			return errors.Wrapf(err, "fails get GVK for inference graph")
		}
		ownerReference := metav1cfg.OwnerReference().
			WithKind(graphGVK.Kind).
			WithAPIVersion(graphGVK.GroupVersion().String()).
			WithName(graph.GetName()).
			WithUID(graph.UID).
			WithBlockOwnerDeletion(true).
			WithController(true)

		// Create a Service Account that can be used to check auth
		saAuthVerifier := corev1cfg.ServiceAccount(saName, graph.GetNamespace()).
			WithOwnerReferences(ownerReference)
		_, err = clientset.CoreV1().ServiceAccounts(graph.GetNamespace()).Apply(ctx, saAuthVerifier, metav1.ApplyOptions{FieldManager: InferenceGraphControllerName})
		if err != nil {
			return errors.Wrapf(err, "fails to apply auth-verifier service account for inference graph")
		}

		// Bind the required privileges to the Service Account
		err = addAuthPrivilegesToGraphServiceAccount(ctx, clientset, graph)
		if err != nil {
			return err
		}
	} else {
		err := removeAuthPrivilegesFromGraphServiceAccount(ctx, clientset, graph)
		if err != nil {
			return err
		}

		err = deleteGraphServiceAccount(ctx, clientset, graph)
		if err != nil {
			return err
		}
	}

	return nil
}

func addAuthPrivilegesToGraphServiceAccount(ctx context.Context, clientset kubernetes.Interface, graph *v1alpha1.InferenceGraph) error {
	clusterRoleBinding, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, constants.InferenceGraphAuthCRBName, metav1.GetOptions{})
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "fails to get cluster role binding kserve-inferencegraph-auth-verifiers while configuring inference graph auth")
	}

	saName := getServiceAccountNameForGraph(graph)
	if apierrors.IsNotFound(err) {
		clusterRoleAuxiliary := rbacv1.ClusterRole{}
		rbRoleRef := rbacv1cfg.RoleRef().
			WithKind("ClusterRole").
			WithName("system:auth-delegator").
			WithAPIGroup(clusterRoleAuxiliary.GroupVersionKind().Group)
		rbSubject := rbacv1cfg.Subject().
			WithKind("ServiceAccount").
			WithNamespace(graph.GetNamespace()).
			WithName(saName)
		crbApply := rbacv1cfg.ClusterRoleBinding(constants.InferenceGraphAuthCRBName).
			WithRoleRef(rbRoleRef).
			WithSubjects(rbSubject)

		_, err = clientset.RbacV1().ClusterRoleBindings().Apply(ctx, crbApply, metav1.ApplyOptions{FieldManager: InferenceGraphControllerName})
		if err != nil {
			return errors.Wrapf(err, "fails to apply kserve-inferencegraph-auth-verifiers ClusterRoleBinding for inference graph")
		}
	} else {
		isPresent := false
		for _, subject := range clusterRoleBinding.Subjects {
			if subject.Kind == "ServiceAccount" && subject.Name == saName && subject.Namespace == graph.GetNamespace() {
				isPresent = true
				break
			}
		}
		if !isPresent {
			clusterRoleBinding.Subjects = append(clusterRoleBinding.Subjects, rbacv1.Subject{
				Kind:      "ServiceAccount",
				Name:      saName,
				Namespace: graph.GetNamespace(),
			})
			_, err = clientset.RbacV1().ClusterRoleBindings().Update(ctx, clusterRoleBinding, metav1.UpdateOptions{FieldManager: InferenceGraphControllerName})
			if err != nil {
				return errors.Wrapf(err, "fails to bind privileges for auth verification to inference graph")
			}
		}
	}

	return nil
}

func removeAuthPrivilegesFromGraphServiceAccount(ctx context.Context, clientset kubernetes.Interface, graph *v1alpha1.InferenceGraph) error {
	clusterRole, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, constants.InferenceGraphAuthCRBName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return errors.Wrapf(err, "fails to get cluster role binding kserve-inferencegraph-auth-verifiers while deconfiguring inference graph auth")
	}

	isPresent := false
	saName := getServiceAccountNameForGraph(graph)
	for idx, subject := range clusterRole.Subjects {
		if subject.Kind == "ServiceAccount" && subject.Name == saName && subject.Namespace == graph.GetNamespace() {
			isPresent = true

			// Remove the no longer needed entry
			clusterRole.Subjects[idx] = clusterRole.Subjects[len(clusterRole.Subjects)-1]
			clusterRole.Subjects = clusterRole.Subjects[:len(clusterRole.Subjects)-1]
			break
		}
	}

	if isPresent {
		_, err = clientset.RbacV1().ClusterRoleBindings().Update(ctx, clusterRole, metav1.UpdateOptions{FieldManager: InferenceGraphControllerName})
		if err != nil {
			return errors.Wrapf(err, "fails to remove privileges for auth verification from inference graph")
		}
	}

	return nil
}

func deleteGraphServiceAccount(ctx context.Context, clientset kubernetes.Interface, graph *v1alpha1.InferenceGraph) error {
	saName := getServiceAccountNameForGraph(graph)
	err := clientset.CoreV1().ServiceAccounts(graph.GetNamespace()).Delete(ctx, saName, metav1.DeleteOptions{})
	if client.IgnoreNotFound(err) != nil {
		return errors.Wrapf(err, "fails to delete service account for inference graph while deconfiguring auth")
	}
	return nil
}

func getServiceAccountNameForGraph(graph *v1alpha1.InferenceGraph) string {
	return graph.GetName() + "-auth-verifier"
}
