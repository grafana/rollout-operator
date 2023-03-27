// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MultiZoneIngesterAutoScaler is a specification for a MultiZoneIngesterAutoScaler resource.
type MultiZoneIngesterAutoScaler struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultiZoneIngesterAutoScalerSpec   `json:"spec"`
	Status MultiZoneIngesterAutoScalerStatus `json:"status"`
}

// MultiZoneIngesterAutoScalerSpec is the spec for a MultiZoneIngesterAutoScaler resource.
type MultiZoneIngesterAutoScalerSpec struct {
	DeploymentName string `json:"deploymentName"`
	Replicas       *int32 `json:"replicas"`
}

// MultiZoneIngesterAutoScalerStatus is the status for a MultiZoneIngesterAutoScaler resource.
type MultiZoneIngesterAutoScalerStatus struct {
	AvailableReplicas int32 `json:"availableReplicas"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MultiZoneIngesterAutoScalerList is a list of MultiZoneIngesterAutoScaler resources
type MultiZoneIngesterAutoScalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []MultiZoneIngesterAutoScaler `json:"items"`
}
