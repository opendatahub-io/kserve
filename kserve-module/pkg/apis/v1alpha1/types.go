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

// +kubebuilder:validation:Enum=Headless;Headed
type RawServiceConfig string

const (
	KserveRawHeadless RawServiceConfig = "Headless"
	KserveRawHeaded   RawServiceConfig = "Headed"
)

// Compile-time check: Kserve must implement common.PlatformObject so the
// orchestrator (ODH Operator) can read status, conditions, and releases
// through a uniform interface across all modules.
var _ common.PlatformObject = &Kserve{}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:validation:XValidation:rule="self.metadata.name == 'default-kserve'",message="Kserve name must be 'default-kserve'"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`,description="Ready"
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`,description="Reason"
type Kserve struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              KserveSpec   `json:"spec,omitempty"`
	Status            KserveStatus `json:"status,omitempty"`
}

type KserveSpec struct {
	common.ManagementSpec `json:",inline"`
	// +kubebuilder:default=Headless
	RawDeploymentServiceConfig RawServiceConfig `json:"rawDeploymentServiceConfig,omitempty"`
	NIM                        NIMSpec          `json:"nim,omitempty"`
	WVA                        WVASpec          `json:"wva,omitempty"`
}

type NIMSpec struct {
	AirGapped bool `json:"airGapped,omitempty"`
	// +kubebuilder:validation:Enum=Managed;Removed
	// +kubebuilder:default=Managed
	ManagementState common.ManagementState `json:"managementState,omitempty"`
}

type WVASpec struct {
	// +kubebuilder:validation:Enum=Managed;Removed
	// +kubebuilder:default=Removed
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

// PlatformObject accessor methods.
// The orchestrator uses these to read/write module status generically:
//   - GetStatus: Phase, Conditions, ObservedGeneration
//   - Get/SetConditions: Ready, ProvisioningSucceeded, Degraded
//   - Get/SetReleaseStatus: deployed component versions (KServe, odh-model-controller)

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

func init() {
	SchemeBuilder.Register(&Kserve{}, &KserveList{})
}
