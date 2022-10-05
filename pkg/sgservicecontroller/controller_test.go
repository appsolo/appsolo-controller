package sgservicecontroller_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/appsolo/appsolo-controller/pkg/sgservicecontroller"
)

const (
	fakeStatefulSetName   = "fake-postgres"
	fakeNamespace         = "fake"
	fakeContainerName     = "fake-postgres-0"
	fakeContainerImage    = "postgres:14.0"
	fakePodWithVolumeName = "fake-pg-data"
	fakePVName            = "fake-pg-data-pvc"
	fakeLabel             = "fake-postgres-label"
	fakeServiceName       = fakeStatefulSetName + "-replicas-besteffort"
)

var (
	statefulSet = &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakeStatefulSetName,
			Namespace: fakeNamespace,
			Labels: map[string]string{
				"app": fakeLabel,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": fakeStatefulSetName},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": fakeStatefulSetName},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  fakeContainerName,
							Image: fakeContainerImage,
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      fakePodWithVolumeName,
									MountPath: "/var/lib/pgdata",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: fakePodWithVolumeName,
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: fakePVName,
								},
							},
						},
					},
				},
			},
		},
		Status: appsv1.StatefulSetStatus{
			ReadyReplicas: 1,
		},
	}
)

func initialKubeClientWithStatefulSet() kubernetes.Interface {
	// create fake statefulSet
	return fake.NewSimpleClientset(
		statefulSet,
	)
}

func TestSGController_Run_ServiceCreateExisting(t *testing.T) {
	kubeClient := initialKubeClientWithStatefulSet()

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	sgController := sgservicecontroller.NewSGServiceController("test", kubeClient, sgServiceControllerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := sgController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	_, err = kubeClient.CoreV1().Services(fakeNamespace).Get(ctx, fakeServiceName, metav1.GetOptions{})
	assert.NoError(t, err, "service should exist")
}

func TestSGController_Run_ServiceCreateNew(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	sgController := sgservicecontroller.NewSGServiceController("test", kubeClient, sgServiceControllerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := sgController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = sgController.HandleStatefulSetWatchEvent(ctx, watch.Event{Type: watch.Added, Object: statefulSet})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Services(fakeNamespace).Get(ctx, fakeServiceName, metav1.GetOptions{})
	assert.NoError(t, err, "service should exist")
}

func TestSGController_Run_SingleReplica(t *testing.T) {
	kubeClient := initialKubeClientWithStatefulSet()

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	sgController := sgservicecontroller.NewSGServiceController("test", kubeClient, sgServiceControllerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := sgController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	statefulSet.Status.ReadyReplicas = 1

	err = sgController.HandleStatefulSetWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: statefulSet})
	assert.NoError(t, err, "watcher should not error")

	service, err := kubeClient.CoreV1().Services(fakeNamespace).Get(ctx, fakeServiceName, metav1.GetOptions{})
	assert.NoError(t, err, "service should exist")

	assert.Equal(t, "master", service.Spec.Selector["role"], "service selector should have role master")
}

func TestSGController_Run_MultiReplica(t *testing.T) {
	kubeClient := initialKubeClientWithStatefulSet()

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	sgController := sgservicecontroller.NewSGServiceController("test", kubeClient, sgServiceControllerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := sgController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	statefulSet.Status.ReadyReplicas = 2

	err = sgController.HandleStatefulSetWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: statefulSet})
	assert.NoError(t, err, "watcher should not error")

	service, err := kubeClient.CoreV1().Services(fakeNamespace).Get(ctx, fakeServiceName, metav1.GetOptions{})
	assert.NoError(t, err, "service should exist")
	assert.Equal(t, "replica", service.Spec.Selector["role"], "service selector should have role replica")
}

func TestSGController_Run_DeleteStatefulSet(t *testing.T) {
	kubeClient := initialKubeClientWithStatefulSet()

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	sgController := sgservicecontroller.NewSGServiceController("test", kubeClient, sgServiceControllerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := sgController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = sgController.HandleStatefulSetWatchEvent(ctx, watch.Event{Type: watch.Deleted, Object: statefulSet})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Services(fakeNamespace).Get(ctx, fakeServiceName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "service should not exist")
}
