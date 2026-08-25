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

package tls

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	apiServerName = "cluster"
)

var watcherLog = ctrl.Log.WithName("tls-profile-watcher")

// ProfileWatcher watches the APIServer CR and triggers a callback when the
// resolved TLS settings (profile + adherence policy) change.
type ProfileWatcher struct {
	client.Client
	RESTConfig       *rest.Config
	InitialSettings  Settings
	OnSettingsChange func(ctx context.Context, oldSettings, newSettings Settings)

	lastSettings   Settings
	baselineSynced bool

	// fetchAdherence overrides cluster adherence reads in tests.
	fetchAdherence func(context.Context, *rest.Config) (string, bool)
}

func (w *ProfileWatcher) readAdherence(ctx context.Context) (string, bool) {
	if w.fetchAdherence != nil {
		return w.fetchAdherence(ctx, w.RESTConfig)
	}
	return fetchTLSAdherence(ctx, w.RESTConfig)
}

func (w *ProfileWatcher) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	if req.Name != apiServerName {
		return reconcile.Result{}, nil
	}

	apiServer := &configv1.APIServer{}
	if err := w.Get(ctx, req.NamespacedName, apiServer); err != nil {
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	adherence, adherenceOK := w.readAdherence(ctx)
	if !adherenceOK {
		watcherLog.Info("Failed to read TLS adherence policy, will retry")
		return reconcile.Result{RequeueAfter: adherenceRequeueDelay}, nil
	}
	currentSettings := settingsFromAPIServer(apiServer, adherence)
	if !w.baselineSynced {
		w.lastSettings = currentSettings
		w.baselineSynced = true
		return reconcile.Result{}, nil
	}
	if w.OnSettingsChange != nil && !settingsEqual(w.lastSettings, currentSettings) {
		old := w.lastSettings
		w.lastSettings = currentSettings
		w.OnSettingsChange(ctx, old, currentSettings)
	}

	return reconcile.Result{}, nil
}

func (w *ProfileWatcher) SetupWithManager(mgr ctrl.Manager) error {
	w.lastSettings = w.InitialSettings

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

// SetupProfileWatcherRestart wraps cancel-context setup and registers the profile watcher.
// When the cluster TLS profile or adherence policy changes, the returned context is cancelled
// so the manager can shut down and restart with updated TLS settings.
//
// Pod restart is intentional for RHOAIENG-78968 (matches odh-model-controller #863): controller-runtime
// applies TLSOpts at listener creation time, so in-process hot-reload would need GetConfigForClient
// wiring for webhook and metrics. Tracked as a follow-up in kserve/kserve#6071.
func SetupProfileWatcherRestart(ctx context.Context, mgr ctrl.Manager, result Result) context.Context {
	if !result.APIAvailable {
		return ctx
	}

	childCtx, cancel := context.WithCancel(ctx)
	watcher := &ProfileWatcher{
		Client:          mgr.GetClient(),
		RESTConfig:      mgr.GetConfig(),
		InitialSettings: result.InitialSettings,
		lastSettings:    result.InitialSettings,
		OnSettingsChange: func(_ context.Context, _, _ Settings) {
			watcherLog.Info("TLS security profile or adherence policy changed, shutting down for restart")
			cancel()
		},
	}
	if err := watcher.SetupWithManager(mgr); err != nil {
		cancel()
		watcherLog.Error(err, "Failed to set up TLS security profile watcher; profile changes will not trigger a restart")
		return ctx
	}
	return childCtx
}
