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

package fixture

import (
	"context"

	"github.com/onsi/gomega"
)

// WithIstioShadowService creates an Istio shadow service in the namespace.
func WithIstioShadowService(svcName string) TestNamespaceOption {
	return func(ctx context.Context, tn *TestNamespace) {
		svc := IstioShadowService(svcName, tn.Name)
		gomega.Expect(tn.client.Create(ctx, svc)).To(gomega.Succeed())
		tn.resources = append(tn.resources, svc)
	}
}
