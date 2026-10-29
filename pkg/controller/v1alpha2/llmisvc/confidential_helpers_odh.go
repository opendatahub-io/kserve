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
	"maps"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/yaml"

	"github.com/kserve/kserve/pkg/constants"
)

const confidentialManagedLabel = "serving.kserve.io/confidential-bare-metal"

// Install the scripts and permissions needed by confidential pods in this namespace.
func (r *LLMISVCReconciler) reconcileConfidentialHelpers(ctx context.Context, namespace string) error {
	bundle := &corev1.ConfigMap{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: constants.KServeNamespace, Name: confidentialHelpersName}, bundle); err != nil {
		return fmt.Errorf("load confidential helpers: %w", err)
	}
	sa := &corev1.ServiceAccount{}
	binding := &rbacv1.RoleBinding{}
	if err := yaml.Unmarshal([]byte(bundle.Data["service-account.yaml"]), sa); err != nil {
		return err
	}
	if err := yaml.Unmarshal([]byte(bundle.Data["role-binding.yaml"]), binding); err != nil {
		return err
	}
	if sa.Name == "" || binding.Name == "" || bundle.Data["link.sh"] == "" || bundle.Data["format-disk.sh"] == "" {
		return errors.New("installed confidential helper definitions are incomplete")
	}
	for i := range binding.Subjects {
		binding.Subjects[i].Namespace = namespace
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cc-storage-link"}, Data: map[string]string{
		"link.sh": bundle.Data["link.sh"], "format-disk.sh": bundle.Data["format-disk.sh"],
	}}
	for _, expected := range []client.Object{cm, sa, binding} {
		expected.SetNamespace(namespace)
		// Shared helpers have no service owner: one service cannot garbage-collect another's dependencies.
		current := expected.DeepCopyObject().(client.Object)
		_, err := controllerutil.CreateOrUpdate(ctx, r.Client, current, func() error {
			if current.GetResourceVersion() != "" && current.GetLabels()[confidentialManagedLabel] != "true" {
				return fmt.Errorf("confidential helper %s/%s already exists and is not managed by KServe", namespace, current.GetName())
			}
			labels := maps.Clone(current.GetLabels())
			if labels == nil {
				labels = map[string]string{}
			}
			labels[confidentialManagedLabel] = "true"
			labels[constants.KubernetesPartOfLabelKey] = constants.LLMInferenceServicePartOfValue
			current.SetLabels(labels)
			current.SetOwnerReferences(nil)
			switch obj := current.(type) {
			case *corev1.ConfigMap:
				obj.Data = cm.Data
			case *corev1.ServiceAccount:
				obj.AutomountServiceAccountToken = sa.AutomountServiceAccountToken
			case *rbacv1.RoleBinding:
				obj.RoleRef = binding.RoleRef
				obj.Subjects = binding.Subjects
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("reconcile confidential helper %s/%s: %w", namespace, expected.GetName(), err)
		}
	}
	return nil
}
