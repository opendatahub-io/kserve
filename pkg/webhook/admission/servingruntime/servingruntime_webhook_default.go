//go:build !distro

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

package servingruntime

import (
	"context"
	"fmt"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
)

// +kubebuilder:webhook:verbs=create;update,path=/validate-serving-kserve-io-v1alpha1-clusterservingruntime,mutating=false,failurePolicy=fail,groups=serving.kserve.io,resources=clusterservingruntimes,versions=v1alpha1,name=clusterservingruntime.kserve-webhook-server.validator

type ClusterServingRuntimeValidator struct {
	Client  client.Client
	Decoder admission.Decoder
}

// Handle validates the incoming request
func (csr *ClusterServingRuntimeValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
	clusterServingRuntime := &v1alpha1.ClusterServingRuntime{}
	if err := csr.Decoder.Decode(req, clusterServingRuntime); err != nil {
		log.Error(err, "Failed to decode cluster serving runtime", "name", clusterServingRuntime.Name)
		return admission.Errored(http.StatusBadRequest, err)
	}

	ExistingRuntimes := &v1alpha1.ClusterServingRuntimeList{}
	if err := csr.Client.List(ctx, ExistingRuntimes); err != nil {
		log.Error(err, "Failed to get cluster serving runtime list")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	// Only validate for priority if the new cluster serving runtime is not disabled
	if clusterServingRuntime.Spec.IsDisabled() {
		return admission.Allowed("")
	}
	existingRuntimeSpec := v1alpha1.ServingRuntimeSpec{}
	for i := range ExistingRuntimes.Items {
		if err := validateModelFormatPrioritySame(&clusterServingRuntime.Spec); err != nil {
			return admission.Denied(fmt.Sprintf(ProrityIsNotSameClusterServingRuntimeError, err.Error(), clusterServingRuntime.Name))
		}
		if err := validateServingRuntimePriority(&clusterServingRuntime.Spec, &ExistingRuntimes.Items[i].Spec, clusterServingRuntime.Name, ExistingRuntimes.Items[i].Name); err != nil {
			return admission.Denied(fmt.Sprintf(InvalidPriorityClusterServingRuntimeError, err.Error(), ExistingRuntimes.Items[i].Name, clusterServingRuntime.Name))
		}
		if clusterServingRuntime.Name == ExistingRuntimes.Items[i].Name {
			existingRuntimeSpec = ExistingRuntimes.Items[i].Spec
		}
	}

	if err := validateMultiNodeSpec(&clusterServingRuntime.Spec, &existingRuntimeSpec); err != nil {
		return admission.Denied(fmt.Sprintf(InvalidMultiNodeSpecError, clusterServingRuntime.Kind, clusterServingRuntime.Name, err.Error()))
	}
	return admission.Allowed("")
}
