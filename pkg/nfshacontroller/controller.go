package nfshacontroller

import (
	"context"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
)

// Options to pass when creating a new nfscontroller
type Option func(nfsc *NfshaController)

// Set an EventRecorder that will be notified of any events that occur
func WithEventRecorder(ev record.EventRecorder) Option {
	return func(nfsc *NfshaController) {
		nfsc.evRecorder = ev
	}
}

func WithPodSelector(opts metav1.ListOptions) Option {
	return func(nfsc *NfshaController) {
		nfsc.podListOpts = opts
	}
}

// Create a new nfscontroller with the given Name, Kubernetes client and opts
// Can be customized by adding Option(s).
func NewNFSHAController(name string, kubeClient kubernetes.Interface, opts ...Option) *NfshaController {
	nsfhaController := &NfshaController{
		name:         name,
		kubeClient:   kubeClient,
		watchBackoff: wait.NewExponentialBackoffManager(100*time.Millisecond, 20*time.Second, 1*time.Minute, 2.0, 1.0, &clock.RealClock{}),
	}

	for _, opt := range opts {
		opt(nsfhaController)
	}

	return nsfhaController
}

// Start monitoring the Kubernetes API for changes to the Pod objects
func (nfsc *NfshaController) Run(ctx context.Context, wg *sync.WaitGroup) error {
	defer wg.Done()
	podUpdates, err := nfsc.watchPods(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up Pod watch: %w", err)
	}

	for {
		var err error
		select {
		case <-ctx.Done():
			log.Debug("context done")
			return ctx.Err()
		case podEv, ok := <-podUpdates:
			if !ok {
				return &unexpectedChannelClose{"Pod updates"}
			}
			err = nfsc.HandlePodWatchEvent(ctx, podEv)
		}

		if err != nil {
			log.WithError(err).Info("error processing event: %w", err)
		}
	}
}

type NfshaController struct {
	// Required settings
	name       string
	kubeClient kubernetes.Interface

	// optional settings
	podListOpts  metav1.ListOptions
	evRecorder   record.EventRecorder
	watchBackoff wait.BackoffManager
}

func (nfsc *NfshaController) watchPods(ctx context.Context) (<-chan watch.Event, error) {
	initialPods, err := nfsc.kubeClient.CoreV1().Pods(metav1.NamespaceAll).List(ctx, nfsc.podListOpts)
	if err != nil {
		return nil, err
	}

	watchOpts := *nfsc.podListOpts.DeepCopy()
	watchOpts.ResourceVersion = initialPods.ResourceVersion
	watchOpts.AllowWatchBookmarks = true

	watchChan := make(chan watch.Event)
	podWatch := func() {
		log.WithField("resource-version", watchOpts.ResourceVersion).Debug("(re-)starting Pod watch")
		w, err := nfsc.kubeClient.CoreV1().Pods(metav1.NamespaceAll).Watch(ctx, watchOpts)
		if err != nil {
			log.WithError(err).Info("Pod watch failed, restarting...")
			return
		}

		defer w.Stop()

		for item := range w.ResultChan() {
			if item.Type == watch.Bookmark {
				watchOpts.ResourceVersion = item.Object.(*corev1.Pod).ResourceVersion
				log.WithField("resource-version", watchOpts.ResourceVersion).Trace("Pod watch resource version updated")
				continue
			}

			watchChan <- item
		}
	}

	go wait.BackoffUntil(podWatch, nfsc.watchBackoff, true, ctx.Done())

	return watchChan, nil
}

func (nfsc *NfshaController) HandlePodWatchEvent(ctx context.Context, ev watch.Event) error {
	switch ev.Type {
	case watch.Error:
		return fmt.Errorf("watch error: %v", ev.Object)
	case watch.Modified:
		nfsc.deleteFromPod(ctx, ev.Object.(*corev1.Pod))
	}

	return nil
}

// deleteFromPod force deletes the given pod if the node status is not ready
func (nfsc *NfshaController) deleteFromPod(ctx context.Context, pod *corev1.Pod) {
	// return if the pod doesn't exist
	_, err := nfsc.kubeClient.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return
	}

	nodeNotReadyEvent := false

	// If it's a test just find the event through a loop as FieldSelector does not work on the fake client
	if nfsc.name == "test" {
		events, _ := nfsc.kubeClient.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{})
		for _, event := range events.Items {
			if event.InvolvedObject.Name == pod.Name && event.InvolvedObject.Kind == "Pod" && event.Reason == "NodeNotReady" {
				nodeNotReadyEvent = true
				break
			}
		}
	} else {
		events, _ := nfsc.kubeClient.CoreV1().Events(pod.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + pod.Name, TypeMeta: metav1.TypeMeta{Kind: "Pod"}})
		for _, item := range events.Items {
			if item.Reason == "NodeNotReady" {
				nodeNotReadyEvent = true
				break
			}
		}
	}

	if !nodeNotReadyEvent {
		log.WithField("pod", pod).Trace("Not deleting pod, node is ready")
		return
	}

	// check if the node is not ready
	node, err := nfsc.kubeClient.CoreV1().Nodes().Get(ctx, pod.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Errorf("Error deleting pod %s/%s", pod.Namespace, pod)
		return
	}

	// search the node.Status.Conditions array for the condition type "Ready"
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == "Ready" {
			if node.Status.Conditions[i].Status == "True" {
				// node is ready, do nothing
				log.WithField("pod", pod).Trace("Not deleting pod, node is ready")
				return
			}
		}
	}

	provisionerName := "openebs.io/nfsrwx"

	// Find bound pvcs with storageclass name
	pvcListWithAnnotation := &corev1.PersistentVolumeClaimList{}

	// Get all pvcs in namespace
	pvcList, err := nfsc.kubeClient.CoreV1().PersistentVolumeClaims(pod.Namespace).List(ctx, metav1.ListOptions{})

	if err != nil {
		log.WithError(err).Errorf("Error deleting pod %s/%s", pod.Namespace, pod)
		return
	}

	for _, pvc := range pvcList.Items {
		annotations := pvc.GetAnnotations()

		for key, value := range annotations {
			if key == "volume.kubernetes.io/storage-provisioner" && value == provisionerName {
				log.WithField("pvc", pvc.Name).Trace("Found pvc with provisioner annotation")
				pvcListWithAnnotation.Items = append(pvcListWithAnnotation.Items, pvc)
			}
		}
	}

	if len(pvcListWithAnnotation.Items) == 0 {
		log.WithField("pod", pod).Trace("Not deleting pod, no pvc found")
		return
	}

	log.WithField("pod", pod.Name).Debug("Force deleting pod")
	// Force delete the pod
	noGracePeriod := int64(0)
	err = nfsc.kubeClient.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{GracePeriodSeconds: &noGracePeriod})
	if err != nil && !errors.IsNotFound(err) {
		if err != nil {
			log.WithError(err).Errorf("Error deleting pod %s/%s", pod.Namespace, pod)
			return
		}
	}

	nfsc.eventf(pod, corev1.EventTypeWarning, "ForceDeleted", "pod deleted because node is not ready and needs to be rescheduled")
}

// submit a new event originating from this controller to the k8s API, if configured.
func (nfsc *NfshaController) eventf(obj runtime.Object, eventtype string, reason string, messageFmt string, args ...interface{}) {
	if nfsc.evRecorder == nil {
		return
	}

	nfsc.evRecorder.Eventf(obj, eventtype, reason, messageFmt, args...)
}

// a channel was closed unexpectedly
type unexpectedChannelClose struct {
	channelName string
}

func (u *unexpectedChannelClose) Error() string {
	return fmt.Sprintf("%s closed unexpectedly", u.channelName)
}
