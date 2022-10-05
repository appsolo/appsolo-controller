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
	fakeConsumerPodName           = "fake-nfs-server-consumer"
	fakeNamespace                 = "fake"
	fakeNoPVCPodName              = "fake-nfs-server-no-pvc"
	fakeConsumerContainerName     = "fake-nfs-consumer"
	fakeConsumerContainerImage    = "nfs-consumer:v3.0.0"
	fakeConsumerPodWithVolumeName = "fake-data-consumer"
	fakeConsumerLabel             = "fake-nfs-consumer-label"
	fakeNode                      = "node1.fake.k8s.io"
	fakeEvent                     = "fake-event"
	fakeClaimName                 = "fake-claim"
	fakeProvisionerName           = "openebs.io/nfsrwx"
)

// Create a fake pod
var (
	pvcBound = &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakeClaimName,
			Namespace: fakeNamespace,
			Annotations: map[string]string{
				"volume.kubernetes.io/storage-provisioner": fakeProvisionerName,
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimBound,
		},
	}

	consumerPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakeConsumerPodName,
			Namespace: fakeNamespace,
			Labels: map[string]string{
				"app": fakeConsumerLabel,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: fakeNode,
			Volumes: []corev1.Volume{
				{
					Name: fakeConsumerPodWithVolumeName,
					VolumeSource: corev1.VolumeSource{
						PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
							ClaimName: fakeClaimName,
						},
					},
				},
			},
			Containers: []corev1.Container{
				{
					Name:  fakeConsumerContainerName,
					Image: fakeConsumerContainerImage,
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      fakeConsumerPodWithVolumeName,
							MountPath: "/var/lib/data",
						},
					},
				},
			},
		},
	}

	podNoPVC = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fakeNoPVCPodName,
			Namespace: fakeNamespace,
			Labels: map[string]string{
				"app": fakeConsumerLabel,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: fakeNode,
			Containers: []corev1.Container{
				{
					Name:  fakeConsumerContainerName,
					Image: fakeConsumerContainerImage,
				},
			},
		},
	}

	event = &corev1.Event{
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Name:      fakeConsumerPodName,
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
		node,
		pvcBound,
		consumerPod,
		podNoPVC,
	)
}

func initialKubeClientWithEvent() kubernetes.Interface {
	// create fake statefulSet
	return fake.NewSimpleClientset(
		node,
		event,
		pvcBound,
		consumerPod,
		podNoPVC,
	)
}

func TestNFSHAController_Run_DeletePod(t *testing.T) {
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeConsumerLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: consumerPod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeConsumerPodName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "NFS consumer pod should not exist")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeNoPVCPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "Non NFS pod should exist")
}

func TestNFSHAController_Run_DeletePodNodeReady(t *testing.T) {
	node.Status.Conditions[1].Status = corev1.ConditionTrue
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeConsumerLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: consumerPod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeConsumerPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "NFS consumer pod should exist")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeNoPVCPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "Non NFS pod should exist")
	node.Status.Conditions[1].Status = corev1.ConditionUnknown
}

func TestNFSHAController_Run_DeletePodNoEvent(t *testing.T) {
	kubeClient := initialKubeClientWithPod()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeConsumerLabel}),
	}

	nsfhaController := nfshacontroller.NewNFSHAController("test", kubeClient, nfshacontrollerOpts...)

	ctx := context.Background()

	reconcileCtx, reconcileStop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer reconcileStop()

	var wg sync.WaitGroup
	wg.Add(1)
	err := nsfhaController.Run(reconcileCtx, &wg)
	assert.EqualError(t, err, "context deadline exceeded")

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Modified, Object: consumerPod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeConsumerPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "NFS consumer pod should exist")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeNoPVCPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "Non NFS pod should exist")
}

func TestNFSHAController_Run_DeleteNonExist(t *testing.T) {
	kubeClient := initialKubeClientWithEvent()

	nfshacontrollerOpts := []nfshacontroller.Option{
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=" + fakeConsumerLabel}),
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
	err = kubeClient.CoreV1().Pods(consumerPod.Namespace).Delete(ctx, consumerPod.Name, metav1.DeleteOptions{GracePeriodSeconds: &noGracePeriod})
	if err != nil {
		assert.NoError(t, err, "delete pod should not error")
	}

	err = nsfhaController.HandlePodWatchEvent(ctx, watch.Event{Type: watch.Deleted, Object: consumerPod})
	assert.NoError(t, err, "watcher should not error")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeConsumerPodName, metav1.GetOptions{})
	assert.True(t, errors.IsNotFound(err), "NFS consumer pod should not exist")

	_, err = kubeClient.CoreV1().Pods(fakeNamespace).Get(ctx, fakeNoPVCPodName, metav1.GetOptions{})
	assert.False(t, errors.IsNotFound(err), "Non NFS pod should exist")
}
