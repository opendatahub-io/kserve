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

package inferenceservice

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	"github.com/kserve/kserve/pkg/constants"
)

const authDelegatorTestNS = "parity-ns"

type authDelegatorISVC struct {
	name           string
	serviceAccount string
	auth           *string
	deleting       bool
}

func (f authDelegatorISVC) build() *v1beta1.InferenceService {
	isvc := &v1beta1.InferenceService{
		ObjectMeta: metav1.ObjectMeta{Name: f.name, Namespace: authDelegatorTestNS},
	}
	isvc.Spec.Predictor.ServiceAccountName = f.serviceAccount
	if f.auth != nil {
		isvc.Annotations = map[string]string{constants.ODHKserveRawAuth: *f.auth}
	}
	if f.deleting {
		isvc.Finalizers = []string{"test.finalizer"}
		isvc.DeletionTimestamp = &metav1.Time{Time: time.Unix(1700000000, 0)}
	}
	return isvc
}

// authDelegatorBinding is the ClusterRoleBinding odh-model-controller builds for a service
// account. It also set metadata.namespace, which the API server drops for cluster-scoped objects.
func authDelegatorBinding(serviceAccount string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "parity-ns-" + serviceAccount + "-auth-delegator"},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", APIGroup: "", Name: serviceAccount, Namespace: "parity-ns"},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:auth-delegator"},
	}
}

func authDelegatorFields(crb *rbacv1.ClusterRoleBinding) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:            crb.Name,
			Labels:          crb.Labels,
			Annotations:     crb.Annotations,
			OwnerReferences: crb.OwnerReferences,
		},
		Subjects: crb.Subjects,
		RoleRef:  crb.RoleRef,
	}
}

func newAuthDelegatorTestReconciler(t *testing.T, objs ...client.Object) (*InferenceServiceReconciler, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1beta1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
	return &InferenceServiceReconciler{Client: cl, Scheme: s}, cl
}

func listAuthDelegatorBindings(t *testing.T, cl client.Client) []rbacv1.ClusterRoleBinding {
	t.Helper()
	crbs := &rbacv1.ClusterRoleBindingList{}
	if err := cl.List(t.Context(), crbs); err != nil {
		t.Fatal(err)
	}
	return crbs.Items
}

// TestAuthDelegatorBindingMatchesOdhModelController pins the binding to the output of
// odh-model-controller's KserveRawClusterRoleBindingReconciler.createDesiredResource (incubating
// 25e10f4) for the same InferenceServices, so bindings it created are adopted unchanged.
func TestAuthDelegatorBindingMatchesOdhModelController(t *testing.T) {
	tests := map[string]struct {
		isvc  authDelegatorISVC
		peers []authDelegatorISVC
		want  *rbacv1.ClusterRoleBinding
	}{
		"auth on default SA": {
			isvc: authDelegatorISVC{auth: ptr.To("true")},
			want: authDelegatorBinding("default"),
		},
		"auth on custom SA": {
			isvc: authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			want: authDelegatorBinding("custom-sa"),
		},
		"auth off": {
			isvc: authDelegatorISVC{},
		},
		"auth True is not exact true": {
			isvc: authDelegatorISVC{auth: ptr.To("True")},
		},
		"auth off with peer sharing custom SA": {
			isvc:  authDelegatorISVC{serviceAccount: "custom-sa"},
			peers: []authDelegatorISVC{{name: "peer", serviceAccount: "custom-sa", auth: ptr.To("true")}},
			want:  authDelegatorBinding("custom-sa"),
		},
		"auth off with peer sharing default SA": {
			isvc:  authDelegatorISVC{},
			peers: []authDelegatorISVC{{name: "peer", auth: ptr.To("true")}},
			want:  authDelegatorBinding("default"),
		},
		"auth off with peer on explicit default SA": {
			isvc:  authDelegatorISVC{},
			peers: []authDelegatorISVC{{name: "peer", serviceAccount: "default", auth: ptr.To("true")}},
			want:  authDelegatorBinding("default"),
		},
		"auth off with deleting peer": {
			isvc:  authDelegatorISVC{serviceAccount: "custom-sa"},
			peers: []authDelegatorISVC{{name: "peer", serviceAccount: "custom-sa", auth: ptr.To("true"), deleting: true}},
		},
		"auth off with peer on another SA": {
			isvc:  authDelegatorISVC{serviceAccount: "custom-sa"},
			peers: []authDelegatorISVC{{name: "peer", serviceAccount: "other-sa", auth: ptr.To("true")}},
		},
		"auth on with peer on another SA": {
			isvc:  authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			peers: []authDelegatorISVC{{name: "peer", serviceAccount: "other-sa", auth: ptr.To("true")}},
			want:  authDelegatorBinding("custom-sa"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewGomegaWithT(t)
			tc.isvc.name = "sklearn"
			isvc := tc.isvc.build()
			live := []*v1beta1.InferenceService{isvc}
			objs := make([]client.Object, 0, len(tc.peers)+1)
			objs = append(objs, isvc)
			for _, peer := range tc.peers {
				built := peer.build()
				objs = append(objs, built)
				if !peer.deleting {
					live = append(live, built)
				}
			}
			r, cl := newAuthDelegatorTestReconciler(t, objs...)

			// odh-model-controller reconciled every InferenceService; a peer may be the one that
			// creates the binding, and the InferenceService itself must not remove it afterwards.
			for _, reconciled := range append(live, isvc) {
				g.Expect(r.reconcileAuthDelegatorBinding(t.Context(), reconciled)).To(gomega.Succeed())
			}

			got := &rbacv1.ClusterRoleBinding{}
			err := cl.Get(t.Context(), client.ObjectKey{Name: "parity-ns-" + predictorServiceAccount(isvc) + "-auth-delegator"}, got)
			if tc.want == nil {
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "unexpected error: %v", err)
				return
			}
			g.Expect(err).NotTo(gomega.HaveOccurred())
			if diff := cmp.Diff(tc.want, authDelegatorFields(got)); diff != "" {
				t.Errorf("binding differs from odh-model-controller (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconcileAuthDelegatorBindingRepairsAndRemoves(t *testing.T) {
	drifted := func(serviceAccount string, mutate func(*rbacv1.ClusterRoleBinding)) *rbacv1.ClusterRoleBinding {
		crb := authDelegatorBinding(serviceAccount)
		mutate(crb)
		return crb
	}
	// Namespace "parity" with service account "ns-custom-sa" produces the same binding name as
	// namespace "parity-ns" with "custom-sa".
	colliding := drifted("custom-sa", func(crb *rbacv1.ClusterRoleBinding) {
		crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "ns-custom-sa", Namespace: "parity"}}
	})
	otherRole := drifted("custom-sa", func(crb *rbacv1.ClusterRoleBinding) {
		crb.RoleRef.Name = "view"
	})

	tests := map[string]struct {
		isvc        authDelegatorISVC
		existing    *rbacv1.ClusterRoleBinding
		want        *rbacv1.ClusterRoleBinding
		wantWritten bool
	}{
		"adopts the binding odh-model-controller created without writing it": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: authDelegatorBinding("custom-sa"),
			want:     authDelegatorBinding("custom-sa"),
		},
		"restores the subjects of the default SA binding": {
			isvc: authDelegatorISVC{auth: ptr.To("true")},
			existing: drifted("default", func(crb *rbacv1.ClusterRoleBinding) {
				crb.Subjects = append(crb.Subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: "intruder", Namespace: "other"})
			}),
			want:        authDelegatorBinding("default"),
			wantWritten: true,
		},
		// odh-model-controller looked the default SA binding up as "<ns>--auth-delegator" and
		// never removed it.
		"removes the default SA binding once auth is off": {
			isvc:     authDelegatorISVC{auth: ptr.To("false")},
			existing: authDelegatorBinding("default"),
		},
		"removes the custom SA binding once auth is off": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa"},
			existing: authDelegatorBinding("custom-sa"),
		},
		"keeps an unrelated binding": {
			isvc:     authDelegatorISVC{},
			existing: authDelegatorBinding("custom-sa"),
			want:     authDelegatorBinding("custom-sa"),
		},
		"leaves a colliding binding of another namespace when auth is on": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: colliding,
			want:     colliding,
		},
		"leaves a colliding binding of another namespace when auth is off": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa"},
			existing: colliding,
			want:     colliding,
		},
		"leaves a binding to another role when auth is on": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: otherRole,
			want:     otherRole,
		},
		"leaves a binding to another role when auth is off": {
			isvc:     authDelegatorISVC{serviceAccount: "custom-sa"},
			existing: otherRole,
			want:     otherRole,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewGomegaWithT(t)
			tc.isvc.name = "sklearn"
			isvc := tc.isvc.build()
			r, cl := newAuthDelegatorTestReconciler(t, isvc, tc.existing.DeepCopy())
			before := &rbacv1.ClusterRoleBinding{}
			g.Expect(cl.Get(t.Context(), client.ObjectKeyFromObject(tc.existing), before)).To(gomega.Succeed())

			g.Expect(r.reconcileAuthDelegatorBinding(t.Context(), isvc)).To(gomega.Succeed())

			crbs := listAuthDelegatorBindings(t, cl)
			if tc.want == nil {
				g.Expect(crbs).To(gomega.BeEmpty())
				return
			}
			g.Expect(crbs).To(gomega.HaveLen(1))
			if diff := cmp.Diff(tc.want, authDelegatorFields(&crbs[0])); diff != "" {
				t.Errorf("binding (-want +got):\n%s", diff)
			}
			if tc.wantWritten {
				g.Expect(crbs[0].ResourceVersion).NotTo(gomega.Equal(before.ResourceVersion))
			} else {
				g.Expect(crbs[0].ResourceVersion).To(gomega.Equal(before.ResourceVersion))
			}
		})
	}
}

func TestReleaseAuthDelegatorBinding(t *testing.T) {
	tests := map[string]struct {
		deleted  authDelegatorISVC
		peers    []authDelegatorISVC
		existing *rbacv1.ClusterRoleBinding
		wantKept bool
	}{
		"deletes the binding after the last InferenceService": {
			deleted:  authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: authDelegatorBinding("custom-sa"),
		},
		"deletes the default SA binding after the last InferenceService": {
			deleted:  authDelegatorISVC{auth: ptr.To("true")},
			existing: authDelegatorBinding("default"),
		},
		"keeps the binding while another auth-enabled InferenceService uses the SA": {
			deleted:  authDelegatorISVC{auth: ptr.To("true")},
			peers:    []authDelegatorISVC{{name: "peer", auth: ptr.To("true")}},
			existing: authDelegatorBinding("default"),
			wantKept: true,
		},
		"deletes the binding when the remaining InferenceService has auth off": {
			deleted:  authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			peers:    []authDelegatorISVC{{name: "peer", serviceAccount: "custom-sa"}},
			existing: authDelegatorBinding("custom-sa"),
		},
		"deletes the binding when the other user is being deleted too": {
			deleted:  authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			peers:    []authDelegatorISVC{{name: "peer", serviceAccount: "custom-sa", auth: ptr.To("true"), deleting: true}},
			existing: authDelegatorBinding("custom-sa"),
		},
		"keeps a colliding binding of another namespace": {
			deleted: authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: func() *rbacv1.ClusterRoleBinding {
				crb := authDelegatorBinding("custom-sa")
				crb.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "ns-custom-sa", Namespace: "parity"}}
				return crb
			}(),
			wantKept: true,
		},
		"keeps a binding to another role": {
			deleted: authDelegatorISVC{serviceAccount: "custom-sa", auth: ptr.To("true")},
			existing: func() *rbacv1.ClusterRoleBinding {
				crb := authDelegatorBinding("custom-sa")
				crb.RoleRef.Name = "view"
				return crb
			}(),
			wantKept: true,
		},
		"nothing to delete": {
			deleted: authDelegatorISVC{auth: ptr.To("true")},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := gomega.NewGomegaWithT(t)
			tc.deleted.name = "sklearn"
			tc.deleted.deleting = true
			isvc := tc.deleted.build()
			objs := make([]client.Object, 0, len(tc.peers)+2)
			objs = append(objs, isvc)
			for _, peer := range tc.peers {
				objs = append(objs, peer.build())
			}
			if tc.existing != nil {
				objs = append(objs, tc.existing.DeepCopy())
			}
			r, cl := newAuthDelegatorTestReconciler(t, objs...)

			g.Expect(r.finalizePlatform(t.Context(), isvc)).To(gomega.Succeed())

			if tc.existing == nil {
				g.Expect(listAuthDelegatorBindings(t, cl)).To(gomega.BeEmpty())
				return
			}
			err := cl.Get(t.Context(), client.ObjectKeyFromObject(tc.existing), &rbacv1.ClusterRoleBinding{})
			if tc.wantKept {
				g.Expect(err).NotTo(gomega.HaveOccurred())
			} else {
				g.Expect(apierrors.IsNotFound(err)).To(gomega.BeTrue(), "unexpected error: %v", err)
			}
		})
	}
}

func TestAuthDelegatorBindingWatch(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	users := []client.Object{
		authDelegatorISVC{name: "auth-custom", serviceAccount: "custom-sa", auth: ptr.To("true")}.build(),
		authDelegatorISVC{name: "no-auth-custom", serviceAccount: "custom-sa"}.build(),
		authDelegatorISVC{name: "auth-default", auth: ptr.To("true")}.build(),
	}
	r, _ := newAuthDelegatorTestReconciler(t, users...)

	requests := r.authDelegatorBindingUsers(t.Context(), authDelegatorBinding("custom-sa"))
	g.Expect(requests).To(gomega.ConsistOf(reconcile.Request{
		NamespacedName: types.NamespacedName{Namespace: authDelegatorTestNS, Name: "auth-custom"},
	}))

	colliding := authDelegatorBinding("custom-sa")
	colliding.Subjects = []rbacv1.Subject{{Kind: "ServiceAccount", Name: "custom-sa", Namespace: "elsewhere"}}
	g.Expect(r.authDelegatorBindingUsers(t.Context(), colliding)).To(gomega.BeEmpty())

	changed := authDelegatorBindingChanged()
	binding := authDelegatorBinding("custom-sa")
	edited := binding.DeepCopy()
	edited.Subjects = append(edited.Subjects, rbacv1.Subject{Kind: "ServiceAccount", Name: "intruder", Namespace: "other"})
	relabelled := binding.DeepCopy()
	relabelled.Labels = map[string]string{"team": "a"}
	otherRole := binding.DeepCopy()
	otherRole.RoleRef.Name = "view"

	g.Expect(changed.Create(event.CreateEvent{Object: binding})).To(gomega.BeFalse())
	g.Expect(changed.Delete(event.DeleteEvent{Object: binding})).To(gomega.BeTrue())
	g.Expect(changed.Delete(event.DeleteEvent{Object: otherRole})).To(gomega.BeFalse())
	g.Expect(changed.Update(event.UpdateEvent{ObjectOld: binding, ObjectNew: edited})).To(gomega.BeTrue())
	g.Expect(changed.Update(event.UpdateEvent{ObjectOld: binding, ObjectNew: relabelled})).To(gomega.BeFalse())
}
