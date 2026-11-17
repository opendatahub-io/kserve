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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kserve/kserve/pkg/constants"
)

func TestResolveModelExpressServerODH(t *testing.T) {
	t.Parallel()
	r := &LLMISVCReconciler{}

	t.Run("verifies an https server with the namespace's KServe CA bundle", func(t *testing.T) {
		t.Parallel()
		server, err := r.resolveModelExpressServer(t.Context(), mxService(t, "hf://org/model", map[string]string{
			constants.ModelExpressAddressAnnotationKey:       "https://mx.mx.svc:8001",
			constants.ModelExpressTokenAudienceAnnotationKey: "modelexpress",
		}))
		require.NoError(t, err)
		assert.Equal(t, &modelExpressServer{
			Address:       "https://mx.mx.svc:8001",
			TokenAudience: "modelexpress",
			CABundle:      &modelExpressCABundle{ConfigMap: "odh-kserve-custom-ca-bundle", Key: "cabundle.crt"},
		}, server)
	})

	t.Run("the annotated CA bundle wins", func(t *testing.T) {
		t.Parallel()
		server, err := r.resolveModelExpressServer(t.Context(), mxService(t, "hf://org/model", map[string]string{
			constants.ModelExpressAddressAnnotationKey:           "https://mx.mx.svc:8001",
			constants.ModelExpressCABundleConfigMapAnnotationKey: "openshift-service-ca.crt",
			constants.ModelExpressCABundleKeyAnnotationKey:       "service-ca.crt",
		}))
		require.NoError(t, err)
		assert.Equal(t, &modelExpressCABundle{ConfigMap: "openshift-service-ca.crt", Key: "service-ca.crt"}, server.CABundle)
	})

	t.Run("adds no CA bundle for a plaintext server", func(t *testing.T) {
		t.Parallel()
		for _, address := range []string{"mx.mx.svc:8001", "http://mx.mx.svc:8001"} {
			server, err := r.resolveModelExpressServer(t.Context(), mxService(t, "hf://org/model", map[string]string{
				constants.ModelExpressAddressAnnotationKey: address,
			}))
			require.NoError(t, err, address)
			assert.Nil(t, server.CABundle, address)
		}
	})

	t.Run("returns annotation errors", func(t *testing.T) {
		t.Parallel()
		_, err := r.resolveModelExpressServer(t.Context(), mxService(t, "hf://org/model", map[string]string{
			constants.ModelExpressAddressAnnotationKey:           "mx.mx.svc:8001",
			constants.ModelExpressCABundleConfigMapAnnotationKey: "mx-ca",
		}))
		assert.ErrorContains(t, err, "requires an https://")

		_, err = r.resolveModelExpressServer(t.Context(), mxService(t, "hf://org/model", nil))
		assert.ErrorContains(t, err, constants.ModelExpressAddressAnnotationKey)
	})
}
