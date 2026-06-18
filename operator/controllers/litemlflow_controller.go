// Package controllers implements the reconciliation logic for LiteMLflow CRs.
package controllers

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	litemlflowv1alpha1 "github.com/gorevds/litemlflow/operator/api/v1alpha1"
)

const (
	// imageRepo is the default container image repository for litemlflow.
	imageRepo = "ghcr.io/gorevds/litemlflow"
	// containerPort is the port the litemlflow container listens on.
	containerPort = 5000
	// dataDir is the data directory path inside the container.
	dataDir = "/data"
	// conditionMissingSecret is the condition type set when a required Secret is absent.
	conditionMissingSecret = "MissingSecret"
	// conditionReady is the condition type set when the StatefulSet has ready replicas.
	conditionReady = "Ready"
)

// LiteMLflowReconciler reconciles LiteMLflow objects.
type LiteMLflowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=litemlflow.dev,resources=litemlflows,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=litemlflow.dev,resources=litemlflows/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=litemlflow.dev,resources=litemlflows/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

// Reconcile performs the reconciliation loop for a LiteMLflow resource.
func (r *LiteMLflowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the LiteMLflow instance.
	lmf := &litemlflowv1alpha1.LiteMLflow{}
	if err := r.Get(ctx, req.NamespacedName, lmf); err != nil {
		if errors.IsNotFound(err) {
			// CR was deleted; owned resources are garbage-collected via OwnerReferences.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get LiteMLflow: %w", err)
	}

	// Phase 1: validate secrets when auth.mode == basic. A missing Secret is
	// not an error here — it sets a MissingSecret condition and reconciliation
	// continues (the pod will stay pending until the Secret appears). But a
	// transient API error (or a failed status patch) is returned so the
	// controller requeues instead of silently dropping the reconcile.
	if err := r.validateBasicAuthSecrets(ctx, lmf); err != nil {
		logger.Error(err, "basic-auth secret validation failed")
		return ctrl.Result{}, fmt.Errorf("validate basic-auth secrets: %w", err)
	}

	// Phase 2: reconcile the headless Service (required by StatefulSet).
	if err := r.reconcileHeadlessService(ctx, lmf); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile headless service: %w", err)
	}

	// Phase 3: reconcile the ClusterIP Service (external access).
	if err := r.reconcileService(ctx, lmf); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile service: %w", err)
	}

	// Phase 4: reconcile the StatefulSet.
	ss, err := r.reconcileStatefulSet(ctx, lmf)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile statefulset: %w", err)
	}

	// Phase 5: update status from StatefulSet readiness.
	if err := r.updateStatus(ctx, lmf, ss); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	logger.Info("reconcile complete",
		"name", lmf.Name,
		"ready", lmf.Status.Ready,
		"readyReplicas", ss.Status.ReadyReplicas,
	)
	return ctrl.Result{}, nil
}

// validateBasicAuthSecrets checks that the referenced Secrets exist when auth.mode == basic.
// On failure it sets a MissingSecret condition on the CR status.
func (r *LiteMLflowReconciler) validateBasicAuthSecrets(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow) error {
	if lmf.Spec.Auth.Mode != "basic" {
		return r.clearCondition(ctx, lmf, conditionMissingSecret)
	}

	missing := []string{}
	for _, ref := range []struct {
		ref  litemlflowv1alpha1.SecretKeyRef
		desc string
	}{
		{lmf.Spec.Auth.BasicUserSecret, "basicUserSecret"},
		{lmf.Spec.Auth.BasicPassHashSecret, "basicPassHashSecret"},
	} {
		if ref.ref.Name == "" {
			continue
		}
		secret := &corev1.Secret{}
		err := r.Get(ctx, types.NamespacedName{
			Namespace: lmf.Namespace,
			Name:      ref.ref.Name,
		}, secret)
		if errors.IsNotFound(err) {
			missing = append(missing, fmt.Sprintf("%s(%s)", ref.desc, ref.ref.Name))
		} else if err != nil {
			return err
		}
	}

	if len(missing) > 0 {
		msg := fmt.Sprintf("missing Secrets: %v", missing)
		return r.setCondition(ctx, lmf, metav1.Condition{
			Type:               conditionMissingSecret,
			Status:             metav1.ConditionTrue,
			Reason:             "SecretNotFound",
			Message:            msg,
			ObservedGeneration: lmf.Generation,
		})
	}
	return r.clearCondition(ctx, lmf, conditionMissingSecret)
}

// reconcileHeadlessService ensures the headless Service for the StatefulSet exists.
func (r *LiteMLflowReconciler) reconcileHeadlessService(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow) error {
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lmf.Name + "-headless",
			Namespace: lmf.Namespace,
			Labels:    commonLabels(lmf),
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "None",
			Selector:  selectorLabels(lmf),
			Ports: []corev1.ServicePort{
				{Name: "http", Port: containerPort, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := controllerutil.SetControllerReference(lmf, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: lmf.Namespace, Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	// Headless services rarely change; only update labels and ports.
	existing.Labels = desired.Labels
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, existing)
}

// reconcileService ensures a ClusterIP Service is present.
func (r *LiteMLflowReconciler) reconcileService(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow) error {
	desired := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lmf.Name,
			Namespace: lmf.Namespace,
			Labels:    commonLabels(lmf),
		},
		Spec: corev1.ServiceSpec{
			Selector: selectorLabels(lmf),
			Ports: []corev1.ServicePort{
				{Name: "http", Port: containerPort, Protocol: corev1.ProtocolTCP},
			},
		},
	}
	if err := controllerutil.SetControllerReference(lmf, desired, r.Scheme); err != nil {
		return err
	}

	existing := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Namespace: lmf.Namespace, Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	existing.Labels = desired.Labels
	existing.Spec.Ports = desired.Spec.Ports
	return r.Update(ctx, existing)
}

// reconcileStatefulSet creates or updates the StatefulSet.
// It returns the current (post-apply) StatefulSet so the caller can read
// .status.readyReplicas.
func (r *LiteMLflowReconciler) reconcileStatefulSet(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow) (*appsv1.StatefulSet, error) {
	desired := DesiredStatefulSet(lmf)
	if err := controllerutil.SetControllerReference(lmf, desired, r.Scheme); err != nil {
		return nil, err
	}

	existing := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Namespace: lmf.Namespace, Name: desired.Name}, existing)
	if errors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil {
			return nil, err
		}
		return desired, nil
	}
	if err != nil {
		return nil, err
	}

	// Mutate only the fields the operator owns.
	existing.Labels = desired.Labels
	existing.Spec.Replicas = desired.Spec.Replicas
	existing.Spec.Template = desired.Spec.Template
	// VolumeClaimTemplates are immutable after creation; don't touch them.
	if err := r.Update(ctx, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DesiredStatefulSet computes the desired StatefulSet for a LiteMLflow CR.
// It is exported so the unit test can call it directly without a running API server.
func DesiredStatefulSet(lmf *litemlflowv1alpha1.LiteMLflow) *appsv1.StatefulSet {
	replicas := lmf.Spec.Replicas
	if replicas == 0 {
		replicas = 1
	}

	image := fmt.Sprintf("%s:%s", imageRepo, lmf.Spec.Version)

	envVars := []corev1.EnvVar{
		{Name: "LITEMLFLOW_ADDR", Value: ":5000"},
		{Name: "LITEMLFLOW_DATA", Value: dataDir},
		{Name: "LITEMLFLOW_AUTH", Value: lmf.Spec.Auth.Mode},
	}

	// Basic-auth env vars from Secrets.
	if lmf.Spec.Auth.Mode == "basic" {
		if lmf.Spec.Auth.BasicUserSecret.Name != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name: "LITEMLFLOW_BASIC_USER",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: lmf.Spec.Auth.BasicUserSecret.Name},
					Key:                  lmf.Spec.Auth.BasicUserSecret.Key,
				}},
			})
		}
		if lmf.Spec.Auth.BasicPassHashSecret.Name != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name: "LITEMLFLOW_BASIC_PASS_HASH",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: lmf.Spec.Auth.BasicPassHashSecret.Name},
					Key:                  lmf.Spec.Auth.BasicPassHashSecret.Key,
				}},
			})
		}
	}

	// S3 artifact backend env vars.
	if lmf.Spec.ArtifactBackend == "s3" {
		envVars = append(envVars,
			corev1.EnvVar{Name: "LITEMLFLOW_ARTIFACT_BACKEND", Value: "s3"},
			corev1.EnvVar{Name: "LITEMLFLOW_S3_ENDPOINT", Value: lmf.Spec.S3.Endpoint},
			corev1.EnvVar{Name: "LITEMLFLOW_S3_BUCKET", Value: lmf.Spec.S3.Bucket},
			corev1.EnvVar{Name: "LITEMLFLOW_S3_REGION", Value: lmf.Spec.S3.Region},
		)
		if lmf.Spec.S3.AccessKeySecret.Name != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name: "LITEMLFLOW_S3_ACCESS_KEY",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: lmf.Spec.S3.AccessKeySecret.Name},
					Key:                  lmf.Spec.S3.AccessKeySecret.Key,
				}},
			})
		}
		if lmf.Spec.S3.SecretKeySecret.Name != "" {
			envVars = append(envVars, corev1.EnvVar{
				Name: "LITEMLFLOW_S3_SECRET_KEY",
				ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: lmf.Spec.S3.SecretKeySecret.Name},
					Key:                  lmf.Spec.S3.SecretKeySecret.Key,
				}},
			})
		}
	}

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      lmf.Name,
			Namespace: lmf.Namespace,
			Labels:    commonLabels(lmf),
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: lmf.Name + "-headless",
			Selector: &metav1.LabelSelector{
				MatchLabels: selectorLabels(lmf),
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: selectorLabels(lmf),
				},
				Spec: corev1.PodSpec{
					// The image runs as distroless nonroot (UID/GID 65532).
					// fsGroup makes the kubelet chown the mounted data PVC to
					// that group so the SQLite store is writable; without it the
					// container cannot create /data and crash-loops.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptrBool(true),
						RunAsUser:    ptrInt64(65532),
						RunAsGroup:   ptrInt64(65532),
						FSGroup:      ptrInt64(65532),
					},
					Containers: []corev1.Container{
						{
							Name:            "litemlflow",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: containerPort, Protocol: corev1.ProtocolTCP},
							},
							Env: envVars,
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/healthz",
										Port: intstr.FromInt32(containerPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       15,
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/readyz",
										Port: intstr.FromInt32(containerPort),
									},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       10,
							},
							Resources: lmf.Spec.Resources,
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: dataDir},
								{Name: "tmp", MountPath: "/tmp"},
							},
						},
					},
					Volumes: []corev1.Volume{
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}

	// Attach a VolumeClaimTemplate for the data PVC.
	storageSize := lmf.Spec.Storage.Size
	if storageSize == "" {
		storageSize = "10Gi"
	}
	pvcSpec := corev1.PersistentVolumeClaimSpec{
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
		Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceStorage: resource.MustParse(storageSize),
			},
		},
	}
	if lmf.Spec.Storage.StorageClassName != "" {
		pvcSpec.StorageClassName = &lmf.Spec.Storage.StorageClassName
	}
	ss.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "data"},
			Spec:       pvcSpec,
		},
	}

	return ss
}

// updateStatus refreshes the CR status fields from the live StatefulSet.
func (r *LiteMLflowReconciler) updateStatus(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow, ss *appsv1.StatefulSet) error {
	// Fetch the latest SS status (the object we reconciled may not have it yet).
	live := &appsv1.StatefulSet{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ss.Namespace, Name: ss.Name}, live); err != nil && !errors.IsNotFound(err) {
		return err
	}

	patch := client.MergeFrom(lmf.DeepCopy())
	lmf.Status.Ready = live.Status.ReadyReplicas >= 1
	lmf.Status.ObservedGeneration = lmf.Generation

	ready := metav1.ConditionFalse
	readyReason := "NotReady"
	readyMsg := "waiting for StatefulSet to have ready replicas"
	if lmf.Status.Ready {
		ready = metav1.ConditionTrue
		readyReason = "Ready"
		readyMsg = "at least one replica is ready"
	}
	setConditionInPlace(&lmf.Status.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             ready,
		Reason:             readyReason,
		Message:            readyMsg,
		ObservedGeneration: lmf.Generation,
	})

	return r.Status().Patch(ctx, lmf, patch)
}

// setCondition upserts a condition and patches the status.
func (r *LiteMLflowReconciler) setCondition(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow, c metav1.Condition) error {
	patch := client.MergeFrom(lmf.DeepCopy())
	setConditionInPlace(&lmf.Status.Conditions, c)
	return r.Status().Patch(ctx, lmf, patch)
}

// clearCondition removes a condition by type and patches the status.
func (r *LiteMLflowReconciler) clearCondition(ctx context.Context, lmf *litemlflowv1alpha1.LiteMLflow, condType string) error {
	patch := client.MergeFrom(lmf.DeepCopy())
	conditions := lmf.Status.Conditions[:0]
	for _, c := range lmf.Status.Conditions {
		if c.Type != condType {
			conditions = append(conditions, c)
		}
	}
	if len(conditions) == len(lmf.Status.Conditions) {
		return nil // nothing changed
	}
	lmf.Status.Conditions = conditions
	return r.Status().Patch(ctx, lmf, patch)
}

// setConditionInPlace upserts c into the conditions slice in place.
func setConditionInPlace(conditions *[]metav1.Condition, c metav1.Condition) {
	c.LastTransitionTime = metav1.Now()
	for i, existing := range *conditions {
		if existing.Type == c.Type {
			if existing.Status != c.Status {
				(*conditions)[i] = c
			} else {
				(*conditions)[i].Message = c.Message
				(*conditions)[i].Reason = c.Reason
				(*conditions)[i].ObservedGeneration = c.ObservedGeneration
			}
			return
		}
	}
	*conditions = append(*conditions, c)
}

// SetupWithManager registers the reconciler with the manager.
func (r *LiteMLflowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&litemlflowv1alpha1.LiteMLflow{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func commonLabels(lmf *litemlflowv1alpha1.LiteMLflow) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "litemlflow",
		"app.kubernetes.io/instance":   lmf.Name,
		"app.kubernetes.io/managed-by": "litemlflow-operator",
		"app.kubernetes.io/version":    lmf.Spec.Version,
	}
}

func ptrBool(b bool) *bool    { return &b }
func ptrInt64(i int64) *int64 { return &i }

func selectorLabels(lmf *litemlflowv1alpha1.LiteMLflow) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":     "litemlflow",
		"app.kubernetes.io/instance": lmf.Name,
	}
}
