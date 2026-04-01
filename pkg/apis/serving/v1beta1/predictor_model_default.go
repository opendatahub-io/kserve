//go:build !distro

/*
Copyright 2021 The KServe Authors.

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

package v1beta1

import (
	"context"
	"sort"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	"github.com/kserve/kserve/pkg/constants"
)

// getClusterServingRuntimes lists and filters ClusterServingRuntimes that support the given model.
func (m *ModelSpec) getClusterServingRuntimes(ctx context.Context, cl client.Client, isMMS bool, isMultinode bool, modelProtocolVersion constants.InferenceServiceProtocol) ([]v1alpha1.SupportedRuntime, error) {
	clusterRuntimes := &v1alpha1.ClusterServingRuntimeList{}
	if err := cl.List(ctx, clusterRuntimes); err != nil {
		return nil, err
	}
	sortClusterServingRuntimeList(clusterRuntimes)

	var clusterSrSpecs []v1alpha1.SupportedRuntime
	for i := range clusterRuntimes.Items {
		crt := &clusterRuntimes.Items[i]
		if !crt.Spec.IsDisabled() && crt.Spec.IsMultiModelRuntime() == isMMS &&
			m.RuntimeSupportsModel(&crt.Spec) && crt.Spec.IsProtocolVersionSupported(modelProtocolVersion) && crt.Spec.IsMultiNodeRuntime() == isMultinode {
			clusterSrSpecs = append(clusterSrSpecs, v1alpha1.SupportedRuntime{Name: crt.GetName(), Spec: crt.Spec})
		}
	}
	sortSupportedRuntimeByPriority(clusterSrSpecs, m.ModelFormat)
	return clusterSrSpecs, nil
}

func sortClusterServingRuntimeList(runtimes *v1alpha1.ClusterServingRuntimeList) {
	sort.Slice(runtimes.Items, func(i, j int) bool {
		if GetProtocolVersionPriority(runtimes.Items[i].Spec.ProtocolVersions) <
			GetProtocolVersionPriority(runtimes.Items[j].Spec.ProtocolVersions) {
			return true
		}
		if GetProtocolVersionPriority(runtimes.Items[i].Spec.ProtocolVersions) >
			GetProtocolVersionPriority(runtimes.Items[j].Spec.ProtocolVersions) {
			return false
		}
		if runtimes.Items[i].CreationTimestamp.Before(&runtimes.Items[j].CreationTimestamp) {
			return false
		}
		if runtimes.Items[j].CreationTimestamp.Before(&runtimes.Items[i].CreationTimestamp) {
			return true
		}
		return runtimes.Items[i].Name < runtimes.Items[j].Name
	})
}
