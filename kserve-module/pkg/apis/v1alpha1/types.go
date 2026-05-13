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
	common.ManagementSpec          `json:",inline"`
	RawDeploymentServiceConfig     string  `json:"rawDeploymentServiceConfig,omitempty"`
	NIM                            NIMSpec `json:"nim,omitempty"`
	WVA                            WVASpec `json:"wva,omitempty"`
}

type NIMSpec struct {
	AirGapped       bool                   `json:"airGapped,omitempty"`
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type WVASpec struct {
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type KserveStatus struct {
	common.Status                `json:",inline"`
	common.ComponentReleaseStatus `json:",inline"`
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