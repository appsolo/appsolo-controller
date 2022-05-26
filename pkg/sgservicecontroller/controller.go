// A sgcontroller monitors Pods and their attached PersistentVolumes
// and removes Pods whose storage is "unhealthy".
//
// In general, the sgcontroller uses two inputs: The Kubernetes API,
// to get information about Pods and their volumes, and a user provided
// stream of "failing" volumes. Once it is notified of a failing volume,
// it will try to delete the currently attached Pod and VolumeAttachment,
// which triggers Kubernetes to recreate the Pod with healthy volumes.
//
// "Failing" in this context means that the current resource user is not
// able to write to the volume, i.e. it is safe to attach the volume from
// another node in ReadWriteOnce scenarios.
package sgservicecontroller

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/clock"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
)

// Options to pass when creating a new sgcontroller
type Option func(sgc *SgController)

// Set an EventRecorder that will be notified about Pod and VolumeAttachment deletions
func WithEventRecorder(ev record.EventRecorder) Option {
	return func(sgc *SgController) {
		sgc.evRecorder = ev
	}
}

func WithStatefulSetSelector(opts metav1.ListOptions) Option {
	return func(sgc *SgController) {
		sgc.statefulSetListOpts = opts
	}
}

// Create a new sgcontroller with the given Name, Kubernetes client and opts
// Can be customized by adding Option(s).
func NewSGServiceController(name string, kubeClient kubernetes.Interface, opts ...Option) *SgController {
	sgController := &SgController{
		name:         name,
		kubeClient:   kubeClient,
		watchBackoff: wait.NewExponentialBackoffManager(100*time.Millisecond, 20*time.Second, 1*time.Minute, 2.0, 1.0, &clock.RealClock{}),
	}

	for _, opt := range opts {
		opt(sgController)
	}

	return sgController
}

// Start monitoring volumes and kill pods that use failing volumes.
//
// This will listen for any updates on: Pods, PVCs, VolumeAttachments (VA) to keep
// it's own "state of the world" up-to-date, without requiring a full re-query of all resources.
func (sgc *SgController) Run(ctx context.Context) error {
	statefulSetUpdates, err := sgc.watchStatefulSets(ctx)
	if err != nil {
		return fmt.Errorf("failed to set up StatefulSet watch: %w", err)
	}

	// Here we update our state-of-the-world and check if we we need to remove any failing volume attachments and pods
	for {
		var err error
		select {
		case <-ctx.Done():
			log.Debug("context done")
			return ctx.Err()
		case statefulSetEv, ok := <-statefulSetUpdates:
			if !ok {
				return &unexpectedChannelClose{"StatefulSet updates"}
			}
			err = sgc.HandleStatefulSetWatchEvent(ctx, statefulSetEv)
		}

		if err != nil {
			return fmt.Errorf("error processing event: %w", err)
		}
	}
}

type SgController struct {
	// Required settings
	name       string
	kubeClient kubernetes.Interface

	// optional settings
	statefulSetListOpts metav1.ListOptions
	evRecorder          record.EventRecorder
	watchBackoff        wait.BackoffManager
}

func (sgc *SgController) watchStatefulSets(ctx context.Context) (<-chan watch.Event, error) {
	initialStatefulSets, err := sgc.kubeClient.AppsV1().StatefulSets(metav1.NamespaceAll).List(ctx, sgc.statefulSetListOpts)
	if err != nil {
		return nil, err
	}

	for i := range initialStatefulSets.Items {
		err = sgc.setupService(ctx, &initialStatefulSets.Items[i])
		if err != nil {
			return nil, err
		}

		sgc.updateFromStatefulSet(&initialStatefulSets.Items[i])
	}

	watchOpts := *sgc.statefulSetListOpts.DeepCopy()
	watchOpts.ResourceVersion = initialStatefulSets.ResourceVersion
	watchOpts.AllowWatchBookmarks = true

	watchChan := make(chan watch.Event)
	statefulSetWatch := func() {
		log.WithField("resource-version", watchOpts.ResourceVersion).Debug("(re-)starting StatefulSet watch")
		w, err := sgc.kubeClient.AppsV1().StatefulSets(metav1.NamespaceAll).Watch(ctx, watchOpts)
		if err != nil {
			log.WithError(err).Info("StatefulSet watch failed, restarting...")
			return
		}

		defer w.Stop()

		for item := range w.ResultChan() {
			if item.Type == watch.Bookmark {
				watchOpts.ResourceVersion = item.Object.(*corev1.Pod).ResourceVersion
				log.WithField("resource-version", watchOpts.ResourceVersion).Trace("StatefulSet watch resource version updated")
				continue
			}

			watchChan <- item
		}
	}

	go wait.BackoffUntil(statefulSetWatch, sgc.watchBackoff, true, ctx.Done())

	return watchChan, nil
}

// function setupService to setup a service
func (sgc *SgController) setupService(ctx context.Context, statefulSet *appsv1.StatefulSet) error {
	// set the service name
	serviceName := statefulSet.Name + "-replica-besteffort"
	// set the service namespace
	serviceNamespace := statefulSet.Namespace

	// return if the service already exists
	_, err := sgc.kubeClient.CoreV1().Services(serviceNamespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	// set the service label
	serviceLabels := map[string]string{
		"app": serviceName,
	}
	// set the selectors
	serviceSelector := statefulSet.Spec.Selector.MatchLabels

	// remove disruptible from the selectors
	delete(serviceSelector, "disruptible")

	// set the service type
	serviceType := corev1.ServiceTypeClusterIP
	// set the service ports
	servicePorts := []corev1.ServicePort{
		{
			Name:       "pgport",
			Port:       5432,
			Protocol:   "TCP",
			TargetPort: intstr.FromString("pgport"),
		},
		{
			Name:       "pgreplication",
			Port:       5433,
			Protocol:   "TCP",
			TargetPort: intstr.FromString("pgreplication"),
		},
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: serviceNamespace,
			Labels:    serviceLabels,
		},
		Spec: corev1.ServiceSpec{
			Type:     serviceType,
			Selector: serviceSelector,
			Ports:    servicePorts,
		},
	}

	// create the service
	_, err = sgc.kubeClient.CoreV1().Services(serviceNamespace).Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		log.WithError(err).Errorf("Error creating service %s/%s", serviceNamespace, serviceName)
		return err
	}
	return nil
}

func (sgc *SgController) HandleStatefulSetWatchEvent(ctx context.Context, ev watch.Event) error {
	switch ev.Type {
	case watch.Error:
		return fmt.Errorf("watch error: %v", ev.Object)
	case watch.Added:
		err := sgc.setupService(ctx, ev.Object.(*appsv1.StatefulSet))
		if err != nil {
			return err
		}
		sgc.updateFromStatefulSet(ev.Object.(*appsv1.StatefulSet))
	case watch.Modified:
		sgc.updateFromStatefulSet(ev.Object.(*appsv1.StatefulSet))
	case watch.Deleted:
		sgc.deleteFromStatefulSet(ev.Object.(*appsv1.StatefulSet))
	}

	return nil
}

func (sgc *SgController) updateFromStatefulSet(statefulSet *appsv1.StatefulSet) {
	// set the service name
	serviceName := statefulSet.Name + "-replica-besteffort"
	// set the service namespace
	serviceNamespace := statefulSet.Namespace
	// error if we cannot get the service
	service, err := sgc.kubeClient.CoreV1().Services(serviceNamespace).Get(context.Background(), serviceName, metav1.GetOptions{})
	if err != nil {
		log.WithError(err).Errorf("Error getting service %s/%s", serviceNamespace, serviceName)
		return
	}

	if statefulSet.Status.ReadyReplicas == 1 {
		service.Spec.Selector["role"] = "primary"

		_, err = sgc.kubeClient.CoreV1().Services(serviceNamespace).Update(context.Background(), service, metav1.UpdateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Error updating service %s/%s", serviceNamespace, serviceName)
		}

		// create an sgc.eventf stating the change to primary
		sgc.eventf(service, corev1.EventTypeNormal, "SetPrimary", "Service %s/%s is now set to primary", statefulSet.Namespace, statefulSet.Name)
	} else if statefulSet.Status.ReadyReplicas > 1 {
		selectors := service.Spec.Selector
		selectors["role"] = "replica"

		_, err = sgc.kubeClient.CoreV1().Services(serviceNamespace).Update(context.Background(), service, metav1.UpdateOptions{})
		if err != nil {
			log.WithError(err).Errorf("Error updating service %s/%s", serviceNamespace, serviceName)
		}
		// create an sgc.eventf stating the change to replica
		sgc.eventf(service, corev1.EventTypeNormal, "SetReplica", "Service %s/%s is now set to replica", statefulSet.Namespace, statefulSet.Name)
	}

	log.WithFields(log.Fields{"resource": "Service", "name": serviceName, "namespace": statefulSet.Namespace}).Trace("update")
}

// if statefulset deleted, delete service
func (sgc *SgController) deleteFromStatefulSet(statefulSet *appsv1.StatefulSet) {
	// set the service name
	serviceName := statefulSet.Name + "-replica-besteffort"
	// set the service namespace
	serviceNamespace := statefulSet.Namespace

	// delete the service
	err := sgc.kubeClient.CoreV1().Services(serviceNamespace).Delete(context.Background(), serviceName, metav1.DeleteOptions{})
	if err != nil {
		log.WithError(err).Errorf("Error deleting service %s/%s", serviceNamespace, serviceName)
	}

	log.WithFields(log.Fields{"resource": "Service", "name": serviceName, "namespace": serviceNamespace}).Trace("delete")
}

// submit a new event originating from this controller to the k8s API, if configured.
func (sgc *SgController) eventf(obj runtime.Object, eventtype, reason, messageFmt string, args ...interface{}) {
	if sgc.evRecorder == nil {
		return
	}

	sgc.evRecorder.Eventf(obj, eventtype, reason, messageFmt, args...)
}

// a channel was closed unexpectedly
type unexpectedChannelClose struct {
	channelName string
}

func (u *unexpectedChannelClose) Error() string {
	return fmt.Sprintf("%s closed unexpectedly", u.channelName)
}
