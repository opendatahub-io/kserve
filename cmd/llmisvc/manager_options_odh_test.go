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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/constants"
)

func TestConfidentialHelperConfigMapCache(t *testing.T) {
	selector := labels.SelectorFromSet(labels.Set{"app.kubernetes.io/part-of": "llminferenceservice"})
	configMaps := &corev1.ConfigMap{}
	opts := ctrl.Options{Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
		configMaps: {Namespaces: map[string]cache.Config{
			constants.KServeNamespace: {FieldSelector: fields.OneTermEqualSelector("metadata.name", constants.InferenceServiceConfigMapName)},
			cache.AllNamespaces:       {LabelSelector: selector},
		}},
		&corev1.Secret{}: {Label: selector},
	}}}
	require.NoError(t, customizeManagerOptions(&opts))
	require.Nil(t, opts.Cache.ByObject[configMaps].Namespaces[constants.KServeNamespace].FieldSelector, "both installed defaults and helper definitions must be readable")
	require.Equal(t, selector, opts.Cache.ByObject[configMaps].Namespaces[cache.AllNamespaces].LabelSelector)
}
