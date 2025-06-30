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

package testing

import (
	"context"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type Config struct {
	ctrlSetupFuncs []SetupWithManagerFunc
	envTestOptions []Option
}

// Client acts as a facade for setting up k8s envtest. It allows to wire controllers under tests through
// a simple builder funcs and configure underlying test environment through Option functions.
// It's composed of k8s client.Client and Cleaner to provide unified way of manipulating resources it the env test cluster.
type Client struct {
	client.Client
	*envtest.Environment
	*Cleaner
}

func (c *Client) DeleteAll(objects ...client.Object) {
	if c.Cleaner == nil {
		c.Cleaner = CreateCleaner(c.Client, c.Config, 10*time.Second, 250*time.Millisecond)
	}

	c.Cleaner.DeleteAll(objects...)
}

// Configure creates a new configuration for the Kubernetes EnvTest.
func Configure(options ...Option) *Config {
	return &Config{
		envTestOptions: options,
	}
}

func (c *Client) UsingExistingCluster() bool {
	envValue, exists := os.LookupEnv("USE_EXISTING_CLUSTER")
	if exists {
		return strings.EqualFold(envValue, "true")
	}

	return ptr.Deref(c.UseExistingCluster, false)
}

// WithControllers register controllers under tests required for the test suite.
func (e *Config) WithControllers(setupFunc ...SetupWithManagerFunc) *Config {
	e.ctrlSetupFuncs = append(e.ctrlSetupFuncs, setupFunc...)

	return e
}

// Start wires controller-runtime manager with controllers which are subject of the tests
// and starts Kubernetes EnvTest to verify their behavior.
func (e *Config) Start(ctx context.Context) *Client {
	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.TimeEncoderOfLayout(time.RFC3339),
	}
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseFlagOptions(&opts)))

	envTest := &envtest.Environment{
		CRDInstallOptions: envtest.CRDInstallOptions{
			ErrorIfPathMissing: true,
			CleanUpAfterUse:    true,
		},
		UseExistingCluster: proto.Bool(false), // TODO(testenv): make it configurable
	}

	for _, opt := range e.envTestOptions {
		opt(envTest)
	}

	cfg, errStart := envTest.Start()
	gomega.Expect(errStart).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())

	cli, errClient := client.New(cfg, client.Options{Scheme: envTest.Scheme})
	gomega.Expect(errClient).NotTo(gomega.HaveOccurred())
	gomega.Expect(cli).NotTo(gomega.BeNil())

	mgr, errMgr := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         envTest.Scheme,
		LeaderElection: false,
	})
	gomega.Expect(errMgr).NotTo(gomega.HaveOccurred())

	for _, setupFunc := range e.ctrlSetupFuncs {
		errSetup := setupFunc(cfg, mgr)
		gomega.Expect(errSetup).NotTo(gomega.HaveOccurred())
	}

	go func() {
		defer ginkgo.GinkgoRecover()
		gomega.Expect(mgr.Start(ctx)).To(gomega.Succeed(), "Failed to start manager")
	}()

	return &Client{
		Client:      cli,
		Environment: envTest,
	}
}

type Option func(target *envtest.Environment)

// WithCRDs adds CRDs to the test environment using paths.
func WithCRDs(paths ...string) Option {
	return func(target *envtest.Environment) {
		target.CRDInstallOptions.Paths = append(target.CRDInstallOptions.Paths, paths...)
	}
}

// WithScheme sets the scheme for the test environment.
func WithScheme(addToScheme ...AddToSchemeFunc) Option {
	return func(target *envtest.Environment) {
		testScheme := runtime.NewScheme()
		for _, add := range addToScheme {
			utilruntime.Must(add(testScheme))
		}
		target.Scheme = testScheme
		target.CRDInstallOptions.Scheme = testScheme
	}
}
