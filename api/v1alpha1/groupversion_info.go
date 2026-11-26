// Package v1alpha1 contains the API types of the cell.
//
// The API group is defined here and only here (placeholder, see O-03).
// +kubebuilder:object:generate=true
// +groupName=cell.internal
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "cell.internal", Version: "v1alpha1"}
	SchemeBuilder runtime.SchemeBuilder
	// AddToScheme registers every type added with register.
	AddToScheme = SchemeBuilder.AddToScheme
)

// register adds the API types of this group to the scheme builder.
func register(objs ...runtime.Object) {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, objs...)
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
}

// Label keys put on everything the platform creates for an app.
var (
	LabelApp      = GroupVersion.Group + "/app"
	LabelRetained = GroupVersion.Group + "/retained"
)
