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

package inferenceservice

import (
	"testing"
	"time"

	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

func permTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1beta1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	return s
}

func makeISVCForPerm(name string, annotations map[string]string, saName string) *v1beta1.InferenceService {
	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "ns1",
			Annotations: annotations,
		},
		Spec: v1beta1.InferenceServiceSpec{
			Predictor: v1beta1.PredictorSpec{},
		},
	}
	if saName != "" {
		isvc.Spec.Predictor.ServiceAccountName = saName
	}
	return isvc
}

func TestReconcilePlatformPermissions_AuthEnabled_NoCRB(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "")
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crb.Subjects).To(HaveLen(1))
	g.Expect(crb.Subjects[0].Name).To(Equal("default"))
	g.Expect(crb.Subjects[0].Namespace).To(Equal("ns1"))
	g.Expect(crb.RoleRef.Name).To(Equal("system:auth-delegator"))
}

func TestReconcilePlatformPermissions_AuthEnabled_CRBExists(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "")
	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crb.Subjects[0].Name).To(Equal("default"))
}

func TestReconcilePlatformPermissions_AuthEnabled_CRBDiffers(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "")
	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "wrong-sa", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crb.Subjects[0].Name).To(Equal("default"), "CRB should be updated")
}

func TestReconcilePlatformPermissions_AuthDisabled_NoOthers_CRBDeleted(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc", nil, "")
	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).To(HaveOccurred(), "CRB should be deleted when no ISVCs need auth")
}

func TestReconcilePlatformPermissions_AuthDisabled_OtherISVCSameSA_CRBKept(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc", nil, "")
	otherISVC := makeISVCForPerm("other-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "")
	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, otherISVC, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).ToNot(HaveOccurred(), "CRB should be kept because another ISVC needs it")
}

func TestReconcilePlatformPermissions_AuthDisabled_OtherISVCDifferentSA_CRBDeleted(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc", nil, "")
	otherISVC := makeISVCForPerm("other-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "custom-sa")
	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, otherISVC, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).To(HaveOccurred(), "CRB should be deleted since the other ISVC uses a different SA")
}

func TestReconcilePlatformPermissions_CustomSA(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "my-sa")
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-my-sa-auth-delegator"}, crb)
	g.Expect(err).ToNot(HaveOccurred())
	g.Expect(crb.Subjects[0].Name).To(Equal("my-sa"))
}

func TestReconcilePlatformPermissions_DeletingISVC_ExcludedFromOthers(t *testing.T) {
	g := NewGomegaWithT(t)
	s := permTestScheme()

	isvc := makeISVCForPerm("test-isvc", nil, "")
	now := metav1.NewTime(time.Now())
	deletingISVC := makeISVCForPerm("deleting-isvc",
		map[string]string{constants.ODHKserveRawAuth: "true"}, "")
	deletingISVC.DeletionTimestamp = &now
	deletingISVC.Finalizers = []string{"test-finalizer"}

	existingCRB := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "ns1-default-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: "default", Namespace: "ns1"},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "system:auth-delegator",
		},
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(isvc, deletingISVC, existingCRB).Build()

	err := reconcilePlatformPermissions(t.Context(), cl, isvc)
	g.Expect(err).ToNot(HaveOccurred())

	crb := &rbacv1.ClusterRoleBinding{}
	err = cl.Get(t.Context(), types.NamespacedName{Name: "ns1-default-auth-delegator"}, crb)
	g.Expect(err).To(HaveOccurred(), "CRB should be deleted since the deleting ISVC is excluded")
}
