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

package distro

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func newWatcherTestScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = configv1.Install(scheme)
	return scheme
}

func TestProfileWatcher_DetectsProfileChange(t *testing.T) {
	intermediate := Settings{
		ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		Adherence:   configv1.TLSAdherencePolicyStrictAllComponents,
	}
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			TLSAdherence:       configv1.TLSAdherencePolicyStrictAllComponents,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(newWatcherTestScheme()).
		WithObjects(apiServer).
		Build()

	changed := false
	watcher := &ProfileWatcher{
		Client:          fakeClient,
		InitialSettings: intermediate,
		lastSettings:    intermediate,
		OnSettingsChange: func(_ context.Context, _, _ Settings) {
			changed = true
		},
	}

	_, err := watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if changed {
		t.Fatal("baseline reconcile should not trigger OnSettingsChange")
	}

	apiServer.Spec.TLSSecurityProfile.Type = configv1.TLSProfileIntermediateType
	if err := fakeClient.Update(context.Background(), apiServer); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	_, err = watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if !changed {
		t.Fatal("expected OnSettingsChange when profile differs from baseline settings")
	}
}

func TestProfileWatcher_NoChangeNoCallback(t *testing.T) {
	intermediate := Settings{
		ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		Adherence:   configv1.TLSAdherencePolicyNoOpinion,
	}
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileIntermediateType},
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(newWatcherTestScheme()).
		WithObjects(apiServer).
		Build()

	called := false
	watcher := &ProfileWatcher{
		Client:          fakeClient,
		InitialSettings: intermediate,
		lastSettings:    intermediate,
		OnSettingsChange: func(_ context.Context, _, _ Settings) {
			called = true
		},
	}

	_, err := watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if called {
		t.Fatal("OnSettingsChange should not fire when settings are unchanged")
	}
}

func TestProfileWatcher_DetectsAdherenceChange(t *testing.T) {
	oldProfile := Settings{
		ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileOldType],
		Adherence:   configv1.TLSAdherencePolicyStrictAllComponents,
	}
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileOldType},
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(newWatcherTestScheme()).
		WithObjects(apiServer).
		Build()

	changed := false
	watcher := &ProfileWatcher{
		Client:          fakeClient,
		InitialSettings: oldProfile,
		lastSettings:    oldProfile,
		OnSettingsChange: func(_ context.Context, _, newSettings Settings) {
			changed = true
			if newSettings.ProfileSpec.MinTLSVersion != configv1.VersionTLS10 {
				t.Fatalf("expected Old profile after adherence change, got %q", newSettings.ProfileSpec.MinTLSVersion)
			}
		},
	}

	_, err := watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if changed {
		t.Fatal("baseline reconcile should not trigger OnSettingsChange")
	}

	apiServer.Spec.TLSAdherence = configv1.TLSAdherencePolicyStrictAllComponents
	if err := fakeClient.Update(context.Background(), apiServer); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	_, err = watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if !changed {
		t.Fatal("expected OnSettingsChange when adherence policy changes resolved settings")
	}
}

func TestProfileWatcher_BaselineSyncNoSpuriousRestart(t *testing.T) {
	resolveSettings := Settings{
		ProfileSpec: *configv1.TLSProfiles[configv1.TLSProfileIntermediateType],
		Adherence:   configv1.TLSAdherencePolicyNoOpinion,
	}
	apiServer := &configv1.APIServer{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.APIServerSpec{
			TLSSecurityProfile: &configv1.TLSSecurityProfile{Type: configv1.TLSProfileModernType},
			TLSAdherence:       configv1.TLSAdherencePolicyNoOpinion,
		},
	}
	fakeClient := fake.NewClientBuilder().
		WithScheme(newWatcherTestScheme()).
		WithObjects(apiServer).
		Build()

	called := false
	watcher := &ProfileWatcher{
		Client:          fakeClient,
		InitialSettings: resolveSettings,
		lastSettings:    resolveSettings,
		OnSettingsChange: func(_ context.Context, _, _ Settings) {
			called = true
		},
	}

	_, err := watcher.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: client.ObjectKey{Name: "cluster"},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if called {
		t.Fatal("baseline reconcile should not restart when resolve-time settings differ")
	}
}
