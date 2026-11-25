package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/marcelheidebrecht/cell-platform/internal/cell"
)

func ptr[T any](v T) *T { return &v }

func TestDeploymentProblem(t *testing.T) {
	healthy := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 2},
		Spec:       appsv1.DeploymentSpec{Replicas: ptr(int32(2))},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2},
	}
	if p := deploymentProblem(healthy); p != "" {
		t.Errorf("healthy deployment: %q", p)
	}
	missingPod := healthy
	missingPod.Status = appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if deploymentProblem(missingPod) == "" {
		t.Error("deployment with 1 of 2 desired pods passed")
	}
	stale := healthy
	stale.Generation = 3
	if deploymentProblem(stale) == "" {
		t.Error("deployment with unobserved generation passed")
	}
}

func TestDaemonSetProblem(t *testing.T) {
	ds := appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Status:     appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberAvailable: 0},
	}
	if daemonSetProblem(ds) == "" {
		t.Error("daemonset without available pod passed")
	}
	ds.Status.NumberAvailable = 1
	if p := daemonSetProblem(ds); p != "" {
		t.Errorf("healthy daemonset: %q", p)
	}
}

func TestCheckWorkloadsCoversDaemonSets(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(&appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{Name: "longhorn-manager", Namespace: "longhorn-system"},
		Status:     appsv1.DaemonSetStatus{DesiredNumberScheduled: 1},
	}).Build()
	if r := checkWorkloads(context.Background(), c, "longhorn-system"); r.state != fail {
		t.Errorf("unavailable daemonset: got %s %s", r.state, r.detail)
	}
}

func TestCheckStorageSnapshotErrors(t *testing.T) {
	sc := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{
		Name:        "local-path",
		Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
	}}
	snapErr := func(err error) client.Client {
		return fake.NewClientBuilder().WithObjects(sc).WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if u, ok := list.(*unstructured.UnstructuredList); ok && u.GetKind() == "VolumeSnapshotClassList" {
					return err
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()
	}
	local := cell.Config{Domain: "x", Storage: "local-path"}
	noAPI := &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: "snapshot.storage.k8s.io", Kind: "VolumeSnapshotClass"}}
	if r := checkStorage(context.Background(), snapErr(noAPI), local); r.state != warn {
		t.Errorf("missing snapshot API on local cell: got %s %s", r.state, r.detail)
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "volumesnapshotclasses"}, "", errors.New("denied"))
	if r := checkStorage(context.Background(), snapErr(forbidden), local); r.state != fail || !strings.Contains(r.detail, "denied") {
		t.Errorf("forbidden snapshot list: got %s %s", r.state, r.detail)
	}
	if r := checkStorage(context.Background(), snapErr(noAPI), cell.Config{Domain: "x", Storage: "longhorn"}); r.state != fail {
		t.Errorf("longhorn without snapshot API: got %s %s", r.state, r.detail)
	}
}

// The backup target secret exists from the install on; it counts as
// configured only with a target in it.
func TestCheckBackupTarget(t *testing.T) {
	secret := func(conf string) client.Client {
		return fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "cell-system", Name: "backup-target"},
			Data: map[string][]byte{"password": []byte("pw"), "rclone.conf": []byte(conf), "root": []byte("b/cell")}}).Build()
	}
	if r := checkBackupTarget(context.Background(), fake.NewClientBuilder().Build()); r.state != warn {
		t.Errorf("no secret: %+v", r)
	}
	if r := checkBackupTarget(context.Background(), secret("")); r.state != warn {
		t.Errorf("no target: %+v", r)
	}
	if r := checkBackupTarget(context.Background(), secret("[target]\ntype = s3\nendpoint = https://s3.test\n")); r.state != ok || r.detail != "https://s3.test/b/cell" {
		t.Errorf("s3 target: %+v", r)
	}
}
