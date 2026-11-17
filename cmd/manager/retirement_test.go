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

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	llmisvcvalidation "github.com/kserve/kserve/pkg/controller/llmisvc/validation"
)

// This uses a separate local API server, never the user's current cluster.
func TestRetirementAdmissionEnforcement(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("set KUBEBUILDER_ASSETS to run local API-server admission tests")
	}
	ctx := t.Context()
	environment := &envtest.Environment{CRDDirectoryPaths: []string{
		filepath.Join("..", "..", "config", "crd", "full", "serving.kserve.io_llminferenceservices.yaml"),
		filepath.Join("..", "..", "config", "crd", "full", "serving.kserve.io_llminferenceserviceconfigs.yaml"),
		filepath.Join("..", "..", "config", "crd", "full", "serving.kserve.io_inferenceservices.yaml"),
	}, ErrorIfCRDPathMissing: true}
	cfg, err := environment.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, environment.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, admissionregistrationv1.AddToScheme(scheme))
	require.NoError(t, v1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	newObject := func(kind, name, namespace string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "serving.kserve.io/v1alpha1", "kind": kind,
			"metadata": map[string]interface{}{"name": name, "namespace": namespace},
			"spec":     map[string]interface{}{"model": map[string]interface{}{"uri": "hf://TinyLlama/TinyLlama-1.1B-Chat-v1.0"}},
		}}
	}
	existing := newObject("LLMInferenceService", "existing", "default")
	require.NoError(t, c.Create(ctx, existing))
	existingConfig := newObject("LLMInferenceServiceConfig", "existing-config", "default")
	require.NoError(t, c.Create(ctx, existingConfig))
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "other-models"}}))
	options := &environment.WebhookInstallOptions
	options.ValidatingWebhooks = retirementWebhookObjects(t)
	server := webhook.NewServer(webhook.Options{
		Host: options.LocalServingHost, Port: options.LocalServingPort, CertDir: options.LocalServingCertDir,
	})
	for _, configuration := range options.ValidatingWebhooks {
		server.Register(*configuration.Webhooks[0].ClientConfig.Service.Path, &admission.Webhook{Handler: &llmisvcvalidation.RemovedFeatureValidator{}})
	}
	serverContext, cancelServer := context.WithCancel(ctx)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Start(serverContext) }()
	t.Cleanup(func() {
		cancelServer()
		require.NoError(t, <-serverDone)
	})
	require.NoError(t, options.ModifyWebhookDefinitions())
	require.NoError(t, options.Install(cfg))
	// Admission registration is asynchronous. Dry-run probes cannot leave objects.
	require.Eventually(t, func() bool {
		err := c.Create(ctx, newObject("LLMInferenceService", "probe", "default"), client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), llmisvcvalidation.RemovedMessage)
	}, 20*time.Second, 100*time.Millisecond)
	for _, kind := range []string{"LLMInferenceService", "LLMInferenceServiceConfig"} {
		for _, namespace := range []string{"default", "other-models"} {
			err := c.Create(ctx, newObject(kind, "new-object", namespace))
			require.True(t, apierrors.IsForbidden(err), "%v", err)
			require.Contains(t, err.Error(), "llm-d Technology Preview has been removed")
		}
	}
	for _, object := range []*unstructured.Unstructured{existing, existingConfig} {
		changed := object.DeepCopy()
		require.NoError(t, unstructured.SetNestedField(changed.Object, int64(2), "spec", "replicas"))
		err := c.Update(ctx, changed)
		require.True(t, apierrors.IsForbidden(err), "%v", err)
		require.Contains(t, err.Error(), llmisvcvalidation.RemovedSpecChangeMessage)
		object.SetLabels(map[string]string{"customer": "retained"})
		object.SetAnnotations(map[string]string{"note": "unmanaged"})
		object.SetFinalizers([]string{"customer.example/cleanup"})
		require.NoError(t, c.Update(ctx, object), "legacy finalizer updates must remain possible")
		if object.GetKind() == "LLMInferenceService" {
			object.Object["status"] = map[string]interface{}{"conditions": []interface{}{map[string]interface{}{
				"type": "Ready", "status": "Unknown", "reason": "Unmanaged",
				"message": "Retired", "lastTransitionTime": metav1.Now().Format(time.RFC3339),
			}}}
			require.NoError(t, c.Status().Update(ctx, object))
		}
		object.SetFinalizers(nil)
		require.NoError(t, c.Update(ctx, object))
		require.NoError(t, c.Delete(ctx, object))
	}
	ordinary := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "ordinary", "namespace": "default"},
	}}
	require.NoError(t, c.Create(ctx, ordinary), "the guard must not block unrelated resources")
	ordinaryService := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "serving.kserve.io/v1beta1", "kind": "InferenceService",
		"metadata": map[string]interface{}{"name": "ordinary-model", "namespace": "default"},
		"spec": map[string]interface{}{"predictor": map[string]interface{}{
			"model": map[string]interface{}{"modelFormat": map[string]interface{}{"name": "sklearn"}},
		}},
	}}
	require.NoError(t, c.Create(ctx, ordinaryService), "ordinary InferenceService creation must remain allowed")
	// Removing the handler must not reopen CREATE admission.
	for _, configuration := range options.ValidatingWebhooks {
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(configuration), configuration))
		for i := range configuration.Webhooks {
			unserved := *configuration.Webhooks[i].ClientConfig.URL + "-not-served"
			configuration.Webhooks[i].ClientConfig.URL = &unserved
		}
		require.NoError(t, c.Update(ctx, configuration))
	}
	require.Eventually(t, func() bool {
		err := c.Create(ctx, newObject("LLMInferenceService", "fail-closed-probe", "default"), client.DryRunAll)
		return apierrors.IsInternalError(err) && strings.Contains(err.Error(), "failed calling webhook")
	}, 20*time.Second, 100*time.Millisecond)
}
