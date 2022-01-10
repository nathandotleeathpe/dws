/*
Copyright 2021 Hewlett Packard Enterprise Development LP
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// StorageTypeLabel is the label key used for tagging Storage resources
	// with a driver specific label. For example: dws.cray.hpe.com/storage=Rabbit
	StorageTypeLabel = "dws.cray.hpe.com/storage"

	// StoragePoolLabelPrefix is the prefix for the label key used for tagging
	// Storage resources with a storage pool label.
	// For example: dws.cray.hpe.com/storage-pool-default=true
	StoragePoolLabelPrefix = "dws.cray.hpe.com/storage-pool-"
)

// StorageDevices contains the details of the storage hardware
type StorageDevices struct {
	// Model is the manufacturer information about the device
	Model string `json:"model,omitempty"`

	// Capacity in bytes of the device. The full capacity may not
	// be usable depending on what the storage driver can provide.
	Capacity int64 `json:"capacity,omitempty"`

	// WearLevel in percent for SSDs
	WearLevel int64 `json:"wearLevel,omitempty"`

	// Status of the individual device
	// +kubebuilder:validation:Enum=Ready;NotReady;Failed;Missing
	Status string `json:"status,omitempty"`
}

// Node provides the status of either a compute or a server
type Node struct {
	// Name is the Kubernetes name of the node
	Name string `json:"name,omitempty"`

	// Status of the node
	// +kubebuilder:validation:Enum=Ready;NotReady;Failed;Missing
	Status string `json:"status,omitempty"`
}

// StorageAccess contains nodes and the protocol that may access the storage
type StorageAccess struct {
	// Protocol is the method that this storage can be accessed
	// +kubebuilder:validation:Enum=PCIe
	Protocol string `json:"protocol,omitempty"`

	// Servers is the list of non-compute nodes that have access to
	// the storage
	Servers []Node `json:"servers,omitempty"`

	// Computes is the list of compute nodes that have access to
	// the storage
	Computes []Node `json:"computes,omitempty"`
}

// StorageData contains the data about the storage
type StorageData struct {
	// Type describes what type of storage this is
	// +kubebuilder:validation:Enum=NVMe
	Type string `json:"type,omitempty"`

	// Devices is the list of physical devices that make up this storage
	Devices []StorageDevices `json:"devices,omitempty"`

	// Access contains the information about where the storage is accessible
	Access StorageAccess `json:"access,omitempty"`

	// Capacity is the number of bytes this storage provides. This is the
	// total accessible bytes as determined by the driver and may be different
	// than the sum of the devices' capacities.
	Capacity int64 `json:"capacity,omitempty"`

	// Status is the overall status of the storage
	// +kubebuilder:validation:Enum=Ready;NotReady;Failed;Missing
	Status string `json:"status,omitempty"`
}

// Storage is the Schema for the storages API
//+kubebuilder:object:root=true
type Storage struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Data StorageData `json:"data,omitempty"`
}

//+kubebuilder:object:root=true

// StorageList contains a list of Storage
type StorageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Storage `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Storage{}, &StorageList{})
}