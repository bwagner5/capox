/*
Copyright 2026.

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

package v1alpha1

import (
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OxideMachineSpec defines the desired state of OxideMachine.
type OxideMachineSpec struct {
	ProviderID string `json:"providerID,omitempty"`

	NCpus    int               `json:"nCPUs"`
	Memory   resource.Quantity `json:"memory"`
	DiskSize resource.Quantity `json:"diskSize"`

	ImageID string `json:"imageID"`
}

type OxideMachineInitializationStatus struct {
	Provisioned *bool `json:"provisioned,omitempty"`
}

// OxideMachineStatus defines the observed state of OxideMachine.
type OxideMachineStatus struct {
	Initialization OxideMachineInitializationStatus `json:"initialization,omitempty"`

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the OxideMachine resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.initialization.provisioned"

// OxideMachine is the Schema for the oxidemachines API
type OxideMachine struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of OxideMachine
	// +required
	Spec OxideMachineSpec `json:"spec"`

	// status defines the observed state of OxideMachine
	// +optional
	Status OxideMachineStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OxideMachineList contains a list of OxideMachine
type OxideMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OxideMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OxideMachine{}, &OxideMachineList{})
}
