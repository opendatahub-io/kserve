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

package llmisvc_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kserve/kserve/pkg/constants"
	"github.com/kserve/kserve/pkg/controller/llmisvc"
	"github.com/kserve/kserve/pkg/controller/llmisvc/fixture"
	pkgtest "github.com/kserve/kserve/pkg/testing"
)

func TestLLMInferenceServiceController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LLMInferenceService Controller Suite")
}

var envTest *pkgtest.Client

var _ = SynchronizedBeforeSuite(func() {
	duration, err := time.ParseDuration(constants.GetEnvOrDefault("ENVTEST_DEFAULT_TIMEOUT", "10s"))
	Expect(err).NotTo(HaveOccurred())
	SetDefaultEventuallyTimeout(duration)
	SetDefaultEventuallyPollingInterval(250 * time.Millisecond)

	By("Setting up the test environment")
	systemNs := constants.KServeNamespace

	llmCtrlFunc := func(cfg *rest.Config, mgr ctrl.Manager) error {
		eventBroadcaster := record.NewBroadcaster()
		clientSet, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())

		llmCtrl := llmisvc.LLMInferenceServiceReconciler{
			Client:    mgr.GetClient(),
			Clientset: clientSet,
			// TODO fix it to be set up similar to main.go, for now it's stub
			EventRecorder: eventBroadcaster.NewRecorder(mgr.GetScheme(), corev1.EventSource{Component: "v1beta1Controllers"}),
		}
		return llmCtrl.SetupWithManager(mgr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	envTest = pkgtest.NewEnvTest().WithControllers(llmCtrlFunc).Start(ctx)
	DeferCleanup(func() {
		cancel()
		Expect(envTest.Stop()).To(Succeed())
	})

	fixture.RequiredResources(context.Background(), envTest.Client, systemNs)
}, func() {})
