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

	configv1 "github.com/openshift/api/config/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

var watcherLog = ctrl.Log.WithName("tls-profile-watcher")

// ProfileWatcher watches the APIServer CR and triggers a callback when the
// resolved TLS settings (profile + adherence policy) change.
type ProfileWatcher struct {
	client.Client
	OnSettingsChange func(ctx context.Context, oldSettings, newSettings Settings)

	lastSettings Settings
}

func (w *ProfileWatcher) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	apiServer := &configv1.APIServer{}
	if err := w.Get(ctx, req.NamespacedName, apiServer); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	currentSettings := settingsFromAPIServer(apiServer)
	if w.OnSettingsChange != nil && !settingsEqual(w.lastSettings, currentSettings) {
		old := w.lastSettings
		w.lastSettings = currentSettings
		w.OnSettingsChange(ctx, old, currentSettings)
	}

	return reconcile.Result{}, nil
}

func (w *ProfileWatcher) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("tls-profile-watcher").
		WithOptions(controller.Options{NeedLeaderElection: boolPtr(false)}).
		For(&configv1.APIServer{}, builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return e.Object.GetName() == apiServerName
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				return e.ObjectNew.GetName() == apiServerName
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return e.Object.GetName() == apiServerName
			},
			GenericFunc: func(e event.GenericEvent) bool {
				return e.Object.GetName() == apiServerName
			},
		})).
		Complete(w)
}

func boolPtr(b bool) *bool {
	return &b
}

// SetupProfileWatcher registers a controller that watches the APIServer CR and
// invokes onChange when the resolved TLS settings (profile + adherence policy)
// change. The caller decides how to react (e.g. cancel a context for restart).
func SetupProfileWatcher(mgr ctrl.Manager, result Result, onChange func(ctx context.Context, oldSettings, newSettings Settings)) error {
	if !result.APIAvailable {
		return nil
	}

	watcher := &ProfileWatcher{
		Client:           mgr.GetClient(),
		lastSettings:     result.InitialSettings,
		OnSettingsChange: onChange,
	}
	return watcher.SetupWithManager(mgr)
}
