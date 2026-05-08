package controllers

import (
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	litemlflowv1alpha1 "github.com/gorevds/litemlflow/operator/api/v1alpha1"
)

// TestDesiredStatefulSet_Basic validates the statefulset builder without an API server.
// This covers the core desired-state computation that the reconciler relies on.
func TestDesiredStatefulSet_Basic(t *testing.T) {
	lmf := minimalLMF("test-lmf", "ml", "v1.0.0-rc1")

	ss := DesiredStatefulSet(lmf)

	if ss.Name != "test-lmf" {
		t.Errorf("StatefulSet name: got %q, want %q", ss.Name, "test-lmf")
	}
	if ss.Namespace != "ml" {
		t.Errorf("StatefulSet namespace: got %q, want %q", ss.Namespace, "ml")
	}
	if *ss.Spec.Replicas != 1 {
		t.Errorf("Replicas: got %d, want 1", *ss.Spec.Replicas)
	}

	containers := ss.Spec.Template.Spec.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %d", len(containers))
	}
	c := containers[0]
	if c.Name != "litemlflow" {
		t.Errorf("container name: got %q, want %q", c.Name, "litemlflow")
	}
	wantImage := "ghcr.io/gorevds/litemlflow:v1.0.0-rc1"
	if c.Image != wantImage {
		t.Errorf("container image: got %q, want %q", c.Image, wantImage)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil {
		t.Error("expected HTTPGet liveness probe")
	}
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil {
		t.Error("expected HTTPGet readiness probe")
	}
}

// TestDesiredStatefulSet_ReplicaDefault validates that replicas=0 in spec defaults to 1.
func TestDesiredStatefulSet_ReplicaDefault(t *testing.T) {
	lmf := minimalLMF("r", "ns", "v0.1.0")
	lmf.Spec.Replicas = 0 // should default to 1

	ss := DesiredStatefulSet(lmf)
	if *ss.Spec.Replicas != 1 {
		t.Errorf("expected replicas=1 for zero-value, got %d", *ss.Spec.Replicas)
	}
}

// TestDesiredStatefulSet_BasicAuth validates that basic-auth env vars are injected.
func TestDesiredStatefulSet_BasicAuth(t *testing.T) {
	lmf := minimalLMF("lmf-auth", "prod", "v1.0.0")
	lmf.Spec.Auth = litemlflowv1alpha1.AuthSpec{
		Mode:                "basic",
		BasicUserSecret:     litemlflowv1alpha1.SecretKeyRef{Name: "lmf-creds", Key: "user"},
		BasicPassHashSecret: litemlflowv1alpha1.SecretKeyRef{Name: "lmf-creds", Key: "pass-hash"},
	}

	ss := DesiredStatefulSet(lmf)
	envMap := envByName(ss.Spec.Template.Spec.Containers[0].Env)

	if v, ok := envMap["LITEMLFLOW_AUTH"]; !ok || v != "basic" {
		t.Errorf("LITEMLFLOW_AUTH: got %q, want %q", v, "basic")
	}
	if _, ok := envMap["LITEMLFLOW_BASIC_USER"]; !ok {
		t.Error("expected LITEMLFLOW_BASIC_USER env var from secretKeyRef")
	}
	if _, ok := envMap["LITEMLFLOW_BASIC_PASS_HASH"]; !ok {
		t.Error("expected LITEMLFLOW_BASIC_PASS_HASH env var from secretKeyRef")
	}
}

// TestDesiredStatefulSet_S3 validates that S3 env vars are injected when backend=s3.
func TestDesiredStatefulSet_S3(t *testing.T) {
	lmf := minimalLMF("lmf-s3", "prod", "v1.0.0")
	lmf.Spec.ArtifactBackend = "s3"
	lmf.Spec.S3 = litemlflowv1alpha1.S3Spec{
		Endpoint:        "https://s3.amazonaws.com",
		Bucket:          "my-bucket",
		Region:          "eu-west-1",
		AccessKeySecret: litemlflowv1alpha1.SecretKeyRef{Name: "lmf-s3", Key: "access"},
		SecretKeySecret: litemlflowv1alpha1.SecretKeyRef{Name: "lmf-s3", Key: "secret"},
	}

	ss := DesiredStatefulSet(lmf)
	envMap := envByName(ss.Spec.Template.Spec.Containers[0].Env)

	for _, key := range []string{
		"LITEMLFLOW_ARTIFACT_BACKEND",
		"LITEMLFLOW_S3_ENDPOINT",
		"LITEMLFLOW_S3_BUCKET",
		"LITEMLFLOW_S3_REGION",
		"LITEMLFLOW_S3_ACCESS_KEY",
		"LITEMLFLOW_S3_SECRET_KEY",
	} {
		if _, ok := envMap[key]; !ok {
			t.Errorf("expected env var %s to be present", key)
		}
	}
}

// TestDesiredStatefulSet_StorageSize validates custom PVC storage size.
func TestDesiredStatefulSet_StorageSize(t *testing.T) {
	lmf := minimalLMF("lmf-stor", "ns", "v1.0.0")
	lmf.Spec.Storage.Size = "50Gi"

	ss := DesiredStatefulSet(lmf)
	if len(ss.Spec.VolumeClaimTemplates) == 0 {
		t.Fatal("expected at least one VolumeClaimTemplate")
	}
	req := ss.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	want := resource.MustParse("50Gi")
	if req.Cmp(want) != 0 {
		t.Errorf("storage request: got %s, want %s", req.String(), want.String())
	}
}

// TestDesiredStatefulSet_StorageDefault validates the default PVC storage size.
func TestDesiredStatefulSet_StorageDefault(t *testing.T) {
	lmf := minimalLMF("lmf-def", "ns", "v1.0.0")
	// Storage.Size intentionally left empty — should default to 10Gi.

	ss := DesiredStatefulSet(lmf)
	if len(ss.Spec.VolumeClaimTemplates) == 0 {
		t.Fatal("expected at least one VolumeClaimTemplate")
	}
	req := ss.Spec.VolumeClaimTemplates[0].Spec.Resources.Requests[corev1.ResourceStorage]
	want := resource.MustParse("10Gi")
	if req.Cmp(want) != 0 {
		t.Errorf("default storage request: got %s, want %s", req.String(), want.String())
	}
}

// TestDesiredStatefulSet_Labels validates that selector labels are consistent.
func TestDesiredStatefulSet_Labels(t *testing.T) {
	lmf := minimalLMF("lmf-labels", "ns", "v1.0.0")
	ss := DesiredStatefulSet(lmf)

	podLabels := ss.Spec.Template.Labels
	selectorLabels := ss.Spec.Selector.MatchLabels

	for k, wantV := range selectorLabels {
		if gotV := podLabels[k]; gotV != wantV {
			t.Errorf("pod label %q: got %q, want %q", k, gotV, wantV)
		}
	}
}

// TestDesiredStatefulSet_ServiceName validates the StatefulSet references the headless service.
func TestDesiredStatefulSet_ServiceName(t *testing.T) {
	lmf := minimalLMF("my-lmf", "ns", "v1.0.0")
	ss := DesiredStatefulSet(lmf)
	want := "my-lmf-headless"
	if ss.Spec.ServiceName != want {
		t.Errorf("ServiceName: got %q, want %q", ss.Spec.ServiceName, want)
	}
}

// TestEnvtest_Skipped shows intent to run envtest when KUBEBUILDER_ASSETS is set.
// Without the env var the test is skipped with a clear diagnostic.
func TestEnvtest_Skipped(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set — skipping envtest integration tests. " +
			"Install kubebuilder and set KUBEBUILDER_ASSETS to run full controller tests " +
			"against a real API server.")
	}
	// When KUBEBUILDER_ASSETS is set, a real envtest suite would start here.
	// This placeholder documents the intent.
	t.Log("KUBEBUILDER_ASSETS is set; full envtest suite would run here.")
}

// ── helpers ────────────────────────────────────────────────────────────────────

func minimalLMF(name, namespace, version string) *litemlflowv1alpha1.LiteMLflow {
	lmf := &litemlflowv1alpha1.LiteMLflow{
		Spec: litemlflowv1alpha1.LiteMLflowSpec{
			Version:  version,
			Replicas: 1,
			Auth:     litemlflowv1alpha1.AuthSpec{Mode: "none"},
		},
	}
	lmf.Name = name
	lmf.Namespace = namespace
	return lmf
}

// envByName flattens a []corev1.EnvVar into a map for easy lookup.
// For secretKeyRef vars, the map value is the empty string (we only check presence).
func envByName(envs []corev1.EnvVar) map[string]string {
	m := make(map[string]string, len(envs))
	for _, e := range envs {
		if e.ValueFrom != nil {
			m[e.Name] = ""
		} else {
			m[e.Name] = e.Value
		}
	}
	return m
}
