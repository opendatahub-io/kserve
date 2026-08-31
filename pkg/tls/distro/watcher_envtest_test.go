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
	"errors"
	"testing"
	"time"

	"github.com/onsi/gomega"
	configv1 "github.com/openshift/api/config/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	pkgtest "github.com/kserve/kserve/pkg/testing"
)

func apiServerCRD() *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "apiservers.config.openshift.io"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "config.openshift.io",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "apiservers",
				Singular: "apiserver",
				Kind:     "APIServer",
				ListKind: "APIServerList",
			},
			Scope: apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{
				Name:    "v1",
				Served:  true,
				Storage: true,
				Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
						Type:                   "object",
						XPreserveUnknownFields: ptr.To(true),
					},
				},
			}},
		},
	}
}

// TestSetupProfileWatcher_ForbiddenList_ManagerStaysUp reproduces the
// CrashLoopBackOff seen on RHOAI 3.6-ea.1: Resolve reports APIAvailable=true
// (a successful GET, or self-heal after a transient error), but the deployed
// ClusterRole lacks list/watch on config.openshift.io/apiservers. Registering
// the profile watcher then starts an APIServer informer whose LIST is Forbidden;
// it never syncs, the controller cache-sync times out, and mgr.Start returns
// fatally, crashing every serving controller on startup.
//
// The manager must instead degrade to the resolved static TLS profile and keep
// running. envtest enforces RBAC by default, so an impersonated user with no
// bindings gives a genuine Forbidden on LIST apiservers.
func TestSetupProfileWatcher_ForbiddenList_ManagerStaysUp(t *testing.T) {
	g := gomega.NewWithT(t)

	env := pkgtest.NewEnvTest().BuildEnvironment()
	env.CRDs = append(env.CRDs, apiServerCRD())
	adminCfg, err := env.Start()
	g.Expect(err).NotTo(gomega.HaveOccurred())
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	g.Expect(clientgoscheme.AddToScheme(scheme)).To(gomega.Succeed())
	g.Expect(configv1.Install(scheme)).To(gomega.Succeed())

	// Impersonate a user with no RBAC bindings: LIST/WATCH apiservers is Forbidden,
	// exactly like a cluster missing the tls-distro list/watch grant.
	mgrCfg := rest.CopyConfig(adminCfg)
	mgrCfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:kserve:no-apiservers-access"}

	mgr, err := ctrl.NewManager(mgrCfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		LeaderElection:         false,
		Controller: crconfig.Controller{
			SkipNameValidation: ptr.To(true),
			CacheSyncTimeout:   5 * time.Second,
		},
	})
	g.Expect(err).NotTo(gomega.HaveOccurred())

	err = SetupProfileWatcher(context.Background(), mgr, Result{APIAvailable: true}, nil)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "watcher setup must not error when APIServer is unwatchable")

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startErr := make(chan error, 1)
	go func() { startErr <- mgr.Start(runCtx) }()

	// A fatal APIServer cache-sync makes mgr.Start return within CacheSyncTimeout.
	// The observation window must exceed it so the failure would surface.
	g.Consistently(func() error {
		select {
		case err := <-startErr:
			if err == nil {
				return errors.New("manager exited cleanly during the observation window; it must keep running")
			}
			return err
		default:
			return nil
		}
	}, 10*time.Second, 500*time.Millisecond).Should(gomega.Succeed(),
		"manager exited early: profile watcher made the Forbidden APIServer watch fatal")
}
