// Package v1alpha1 contains the API types of the cell.
//
// The API group is defined here and only here (placeholder, see O-03).
// +kubebuilder:object:generate=true
// +groupName=cell.internal
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "cell.internal", Version: "v1alpha1"}
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}
	AddToScheme   = SchemeBuilder.AddToScheme
)

// Label keys put on everything the platform creates for an app.
var (
	LabelApp      = GroupVersion.Group + "/app"
	LabelRetained = GroupVersion.Group + "/retained"
)
