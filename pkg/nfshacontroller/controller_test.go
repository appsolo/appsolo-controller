package nfshacontroller_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/appsolo-com/appsolo-controller/pkg/nfshacontroller"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	fakePodName           = "fake-nfs-server-provisioner"
	fakeNamespace         = "fake"
	fakeContainerName     = "fake-nfs-provisioner"
	fakeContainerImage    = "nfs-provisioner:v3.0.0"
	fakePodWithVolumeName = "fake-data"
	fakeLabel             = "fake-nfs-provisioner-label"
	fakeNode              = "node1.fake.k8s.io"
	fakeEvent             = "fake-event"
)

// Create a fake pod
var (
	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakePodName,
			Namespace: fakeNamespace,
			Labels: map[string]string{
				"app": fakeLabel,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: fakeNode,
			Containers: []corev1.Container{
				{
					Name:  fakeContainerName,
					Image: fakeContainerImage,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      fakePodWithVolumeName,
							MountPath: "/var/lib/data",
						},
					},
				},
			},
		},
	}

	event = &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Name:      fakePodName,
			Namespace: fakeNamespace,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakeEvent,
			Namespace: fakeNamespace,
		},
		TypeMeta: metav1.TypeMeta{
			Kind: "Pod",
		},
		Reason: "NodeNotReady",
	}

	node = &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: fakeNode,
		},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeDiskPressure,
					Status: corev1.ConditionUnknown,
				},
				{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionUnknown,
				},
				{
					Type:   corev1.NodeMemoryPressure,
					Status: corev1.ConditionUnknown,
				},
			},
		},
	}
)

func initialKubeClientWithPod() kubernetes.Interface {
	// create fake statefulSet
	return fake.NewSimpleClientset(
		pod,
		node,
	)
}

func initialKubeClientWithEvent() kubernetes.Interface {
	// create fake statefulSet
	return fake.NewSimpleClientset(
		pod,
		node,
		event,
	)
}

func TestNFSHAController_Run_DeletePod(t *testing.T) {
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: pod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "pod should not exist")
}

func TestNFSHAController_Run_DeletePodNodeReady(t *testing.T) {
	node.Status.Conditions[1].Status = corev1.ConditionTrue
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: pod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "pod should exist")
}

func TestNFSHAController_Run_DeletePodNoEvent(t *testing.T) {
	kubeClient := initialKubeClientWithPod()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: pod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakePodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "pod should exist")
}

func TestNFSHAController_Run_DeleteNonExist(t *testing.T) {
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	noGracePeriod := int64(0)
	err = kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &noGracePeriod})
	if err != nil {
		assert.NoError(t, err, "delete pod should not error")
	}

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Deleted, Object: pod})
	assert.NoError(t, err, "watcher should not error")
}
