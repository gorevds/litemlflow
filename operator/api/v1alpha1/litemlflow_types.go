// Package v1alpha1 contains the v1alpha1 API types for the LiteMLflow operator.
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LiteMLflowSpec defines the desired state of a LiteMLflow instance.
type LiteMLflowSpec struct {
	// Version is the litemlflow image tag to deploy, e.g. "v1.0.0-rc1".
	// +kubebuilder:validation:MinLength=1
	Version string `json:"version"`

	// Replicas is the desired pod count. Must be 1 (SQLite single-writer constraint).
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1
	Replicas int32 `json:"replicas,omitempty"`

	// Storage configures the PersistentVolumeClaim for the data directory.
	Storage StorageSpec `json:"storage,omitempty"`

	// Auth configures the authentication mode.
	Auth AuthSpec `json:"auth,omitempty"`

	// ArtifactBackend selects the artifact storage backend: "fs" or "s3".
	// +kubebuilder:validation:Enum=fs;s3
	// +kubebuilder:default=fs
	ArtifactBackend string `json:"artifactBackend,omitempty"`

	// S3 configures the S3-compatible artifact backend (only used when artifactBackend="s3").
	S3 S3Spec `json:"s3,omitempty"`

	// Resources sets the CPU/memory requests and limits for the litemlflow container.
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// StorageSpec configures the persistent volume claim.
type StorageSpec struct {
	// Size is the PVC storage request, e.g. "20Gi".
	// +kubebuilder:default="10Gi"
	Size string `json:"size,omitempty"`

	// StorageClassName is the name of the StorageClass to use.
	// An empty string uses the cluster default.
	StorageClassName string `json:"storageClassName,omitempty"`
}

// AuthSpec configures the authentication mode.
type AuthSpec struct {
	// Mode is the authentication mode: "none", "basic", or "oidc".
	// +kubebuilder:validation:Enum=none;basic;oidc
	// +kubebuilder:default=none
	Mode string `json:"mode,omitempty"`

	// OIDCIssuer is the OIDC issuer URL (used when mode="oidc").
	OIDCIssuer string `json:"oidcIssuer,omitempty"`

	// BasicUserSecret references the Secret key holding the basic-auth username.
	BasicUserSecret SecretKeyRef `json:"basicUserSecret,omitempty"`

	// BasicPassHashSecret references the Secret key holding the bcrypt-hashed password.
	BasicPassHashSecret SecretKeyRef `json:"basicPassHashSecret,omitempty"`
}

// SecretKeyRef is a reference to a key in a Kubernetes Secret.
type SecretKeyRef struct {
	// Name is the name of the Secret.
	Name string `json:"name,omitempty"`
	// Key is the key within the Secret.
	Key string `json:"key,omitempty"`
}

// S3Spec configures the optional S3 artifact backend.
type S3Spec struct {
	// Endpoint is the S3-compatible endpoint URL (e.g. "https://s3.amazonaws.com").
	Endpoint string `json:"endpoint,omitempty"`
	// Bucket is the S3 bucket name.
	Bucket string `json:"bucket,omitempty"`
	// Region is the AWS / MinIO region.
	Region string `json:"region,omitempty"`
	// AccessKeySecret references the Secret key holding the S3 access key ID.
	AccessKeySecret SecretKeyRef `json:"accessKeySecret,omitempty"`
	// SecretKeySecret references the Secret key holding the S3 secret access key.
	SecretKeySecret SecretKeyRef `json:"secretKeySecret,omitempty"`
}

// LiteMLflowStatus defines the observed state of a LiteMLflow instance.
type LiteMLflowStatus struct {
	// Ready is true when at least one pod is ready.
	Ready bool `json:"ready,omitempty"`

	// ObservedGeneration is the .metadata.generation last reconciled.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions holds detailed status conditions (e.g. MissingSecret).
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Version",type="string",JSONPath=".spec.version"
// +kubebuilder:printcolumn:name="Ready",type="boolean",JSONPath=".status.ready"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LiteMLflow is the Schema for the litemlflows API.
type LiteMLflow struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LiteMLflowSpec   `json:"spec,omitempty"`
	Status LiteMLflowStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LiteMLflowList contains a list of LiteMLflow.
type LiteMLflowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LiteMLflow `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LiteMLflow{}, &LiteMLflowList{})
}
