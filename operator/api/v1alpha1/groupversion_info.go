// Package v1alpha1 contains the v1alpha1 API types for the LiteMLflow operator.
//
// +groupName=litemlflow.dev
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version for this package.
	GroupVersion = schema.GroupVersion{Group: "litemlflow.dev", Version: "v1alpha1"}

	// SchemeBuilder adds the types in this package to a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this package to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
