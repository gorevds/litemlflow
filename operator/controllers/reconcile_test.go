package controllers

// Guards independent-review P2: Reconcile must requeue on a transient API error
// during basic-auth secret validation (it previously logged and swallowed the
// error, dropping the reconcile), while a genuinely MISSING secret stays a
// non-fatal condition that lets reconciliation continue.

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	litemlflowv1alpha1 "github.com/gorevds/litemlflow/operator/api/v1alpha1"
)

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := litemlflowv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func basicAuthLMF() *litemlflowv1alpha1.LiteMLflow {
	lmf := minimalLMF("lmf", "ns", "v1.0.0")
	lmf.Spec.Auth = litemlflowv1alpha1.AuthSpec{
		Mode:                "basic",
		BasicUserSecret:     litemlflowv1alpha1.SecretKeyRef{Name: "creds", Key: "user"},
		BasicPassHashSecret: litemlflowv1alpha1.SecretKeyRef{Name: "creds", Key: "passhash"},
	}
	return lmf
}

func TestReconcileRequeuesOnTransientSecretError(t *testing.T) {
	scheme := testScheme(t)
	lmf := basicAuthLMF()

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(lmf).
		WithStatusSubresource(&litemlflowv1alpha1.LiteMLflow{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Secret); ok {
					return apierrors.NewServiceUnavailable("apiserver down")
				}
				return cl.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	r := &LiteMLflowReconciler{Client: c, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "lmf"}})
	if err == nil {
		t.Fatal("expected Reconcile to return an error (requeue) on a transient Secret Get failure")
	}
}

func TestReconcileContinuesOnMissingSecret(t *testing.T) {
	scheme := testScheme(t)
	lmf := basicAuthLMF()

	// No Secret seeded → Get returns IsNotFound. This must NOT fail the
	// reconcile; it sets a MissingSecret condition and proceeds.
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(lmf).
		WithStatusSubresource(&litemlflowv1alpha1.LiteMLflow{}).
		Build()

	r := &LiteMLflowReconciler{Client: c, Scheme: scheme}
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "lmf"}})
	if err != nil {
		t.Fatalf("missing Secret should not fail reconcile, got: %v", err)
	}
	// Secrets are not watched, so the reconcile must schedule a re-check or
	// the MissingSecret condition never clears once the Secret is created.
	if res.RequeueAfter <= 0 {
		t.Errorf("RequeueAfter = %v, want > 0 while a Secret is missing", res.RequeueAfter)
	}

	got := &litemlflowv1alpha1.LiteMLflow{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: "lmf"}, got); err != nil {
		t.Fatalf("get CR: %v", err)
	}
	found := false
	for _, cond := range got.Status.Conditions {
		if cond.Type == conditionMissingSecret {
			found = true
		}
	}
	if !found {
		t.Error("expected a MissingSecret condition when the referenced Secret is absent")
	}
}

func TestDesiredStatefulSetSetsNonrootSecurityContext(t *testing.T) {
	ss := DesiredStatefulSet(minimalLMF("sec", "ns", "v1.0.0"))
	sc := ss.Spec.Template.Spec.SecurityContext
	if sc == nil {
		t.Fatal("pod SecurityContext is nil; the distroless nonroot image needs fsGroup to write the data PVC")
	}
	if sc.FSGroup == nil || *sc.FSGroup != 65532 {
		t.Errorf("FSGroup = %v, want 65532 (so the kubelet chowns the data PVC for the nonroot user)", sc.FSGroup)
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("RunAsNonRoot should be true")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
		t.Errorf("RunAsUser = %v, want 65532", sc.RunAsUser)
	}
}

// An unparseable storage size used to panic in resource.MustParse inside
// DesiredStatefulSet, crashing the operator for every CR. It must instead be a
// terminal reconcile error.
func TestReconcileInvalidStorageSizeIsTerminalError(t *testing.T) {
	scheme := testScheme(t)
	lmf := minimalLMF("lmf", "ns", "v1.0.0")
	lmf.Spec.Storage.Size = "ten gigs"
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(lmf).
		WithStatusSubresource(&litemlflowv1alpha1.LiteMLflow{}).
		Build()

	r := &LiteMLflowReconciler{Client: c, Scheme: scheme}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "lmf"}})
	if err == nil {
		t.Fatal("expected an error for an invalid storage size")
	}
	if !errors.Is(err, reconcile.TerminalError(nil)) {
		t.Errorf("expected a terminal error, got: %v", err)
	}
}

func TestDesiredStatefulSetContainerIsRestricted(t *testing.T) {
	ss := DesiredStatefulSet(minimalLMF("sec", "ns", "v1.0.0"))
	csc := ss.Spec.Template.Spec.Containers[0].SecurityContext
	if csc == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if csc.AllowPrivilegeEscalation == nil || *csc.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation should be false")
	}
	if csc.ReadOnlyRootFilesystem == nil || !*csc.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem should be true")
	}
	if csc.Capabilities == nil || len(csc.Capabilities.Drop) != 1 || csc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop = %v, want [ALL]", csc.Capabilities)
	}
	if sp := ss.Spec.Template.Spec.SecurityContext.SeccompProfile; sp == nil || sp.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("pod SeccompProfile = %v, want RuntimeDefault", sp)
	}
}
