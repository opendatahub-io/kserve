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

// +kubebuilder:object:generate=true
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/opendatahub-io/odh-platform-utilities/api/common"
)

const (
	KserveKind         = "Kserve"
	KserveInstanceName = "default-kserve"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default-kserve'",message="Kserve name must be 'default-kserve'"
type Kserve struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KserveSpec   `json:"spec,omitempty"`
	Status            KserveStatus `json:"status,omitempty"`
}

type KserveSpec struct {
	common.ManagementSpec      `json:",inline"`
	RawDeploymentServiceConfig string              `json:"rawDeploymentServiceConfig,omitempty"`
	NIM                        NIMSpec             `json:"nim,omitempty"`
	ModelsAsService            ModelsAsServiceSpec `json:"modelsAsService,omitempty"`
	WVA                        WVASpec             `json:"wva,omitempty"`
}

type NIMSpec struct {
	AirGapped bool `json:"airGapped,omitempty"`
	// +kubebuilder:validation:Enum=Managed;Removed
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type ModelsAsServiceSpec struct {
	// +kubebuilder:validation:Enum=Managed;Removed
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type WVASpec struct {
	// +kubebuilder:validation:Enum=Managed;Removed
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type KserveStatus struct {
	common.Status                 `json:",inline"`
	common.ComponentReleaseStatus `json:",inline"`
}

// PlatformObject interface implementation

func (k *Kserve) GetStatus() *common.Status {
	return &k.Status.Status
}

func (k *Kserve) GetConditions() []common.Condition {
	return k.Status.Conditions
}

func (k *Kserve) SetConditions(conditions []common.Condition) {
	k.Status.Conditions = conditions
}

func (k *Kserve) GetReleaseStatus() *common.ComponentReleaseStatus {
	return &k.Status.ComponentReleaseStatus
}

func (k *Kserve) SetReleaseStatus(status common.ComponentReleaseStatus) {
	k.Status.ComponentReleaseStatus = status
}

// GetManagementState returns the management state from spec, defaulting to Managed.
func GetManagementState(kserve *Kserve) common.ManagementState {
	if kserve.Spec.ManagementState != "" {
		return kserve.Spec.ManagementState
	}
	return common.Managed
}

// +kubebuilder:object:root=true
type KserveList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kserve `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Kserve{}, &KserveList{})
}
