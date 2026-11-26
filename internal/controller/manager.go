package controller

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	cellv1 "github.com/eigen-hq/cell-platform/api/v1alpha1"
	"github.com/eigen-hq/cell-platform/internal/cell"
)

// ManagerOptions fit the operator's cache to its rights (E-70). The secret
// watch covers only the system namespace; the types the operator reads
// without watching them come straight from the API server, and a cached read
// of any other type fails instead of starting a cluster-wide watch the
// operator may not hold.
func ManagerOptions(scheme *runtime.Scheme) ctrl.Options {
	return ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&corev1.Secret{}: {Namespaces: map[string]cache.Config{cell.SystemNamespace: {}}},
			},
			ReaderFailOnMissingInformer: true,
		},
		Client: client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{
			&corev1.Secret{}, &corev1.ConfigMap{}, &corev1.Pod{}, &corev1.PersistentVolumeClaim{}, &corev1.Namespace{},
			&appsv1.StatefulSet{}, &batchv1.CronJob{}, &networkingv1.Ingress{}, &cellv1.Task{},
		}}},
	}
}
