package main

import (
	"context"
	"os/signal"
	"sync"
	"syscall"

	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/appsolo-com/appsolo-controller/pkg/consts"
	"github.com/appsolo-com/appsolo-controller/pkg/k8s"
	"github.com/appsolo-com/appsolo-controller/pkg/nfshacontroller"
	"github.com/appsolo-com/appsolo-controller/pkg/sgservicecontroller"
)

type args struct {
	loglevel   int32
	kubeconfig string
	// enableLeaderElection       bool
	// leaderElectionResourceName string
	// leaderElectionLeaseName    string
	// leaderElectionNamespace    string
	// leaderElectionHealthzPort  int
}

func main() {
	var wg sync.WaitGroup

	args := parseArgs()

	log.SetLevel(log.Level(args.loglevel))
	log.WithField("version", consts.Version).Infof("starting " + consts.Name)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	kubeClient, err := k8s.Clientset(args.kubeconfig)
	if err != nil {
		log.WithError(err).Fatal("failed to create kubernetes client")
	}

	recorder := eventRecorder(kubeClient)

	// Not currently needed
	// if args.enableLeaderElection {
	// 	leaderElector, err := leaderElector(ctx, kubeClient, args.leaderElectionLeaseName, args.leaderElectionNamespace, args.leaderElectionResourceName, args.leaderElectionHealthzPort, recorder)
	// 	if err != nil {
	// 		log.WithError(err).Fatal("failed to create leader elector")
	// 	}
	// 	sgServiceControllerOpts = append(sgServiceControllerOpts, sgservicecontroller.WithLeaderElector(leaderElector))
	// }

	sgServiceControllerOpts := []sgservicecontroller.Option{
		sgservicecontroller.WithEventRecorder(recorder),
		sgservicecontroller.WithStatefulSetSelector(metav1.ListOptions{LabelSelector: "app=StackGresCluster"}),
	}

	sgController := sgservicecontroller.NewSGServiceController(consts.Name, kubeClient, sgServiceControllerOpts...)

	wg.Add(1)
	go func() {
		if err := sgController.Run(ctx, &wg); err != nil {
			log.WithError(err).Fatal("failed to run sgservicecontroller")
		}
	}()

	nfshaControllerOpts := []nfshacontroller.Option{
		nfshacontroller.WithEventRecorder(recorder),
		nfshacontroller.WithPodSelector(metav1.ListOptions{LabelSelector: "app=nfs-server-provisioner"}),
	}

	nfshaController := nfshacontroller.NewNFSHAController(consts.Name, kubeClient, nfshaControllerOpts...)

	wg.Add(1)
	go func() {
		if err := nfshaController.Run(ctx, &wg); err != nil {
			log.WithError(err).Fatal("failed to run NFS HA Controller")
		}
	}()

	wg.Wait()
}

// func leaderElector(ctx context.Context, kubeClient kubernetes.Interface, identity, namespace, name string, healthPort int, recorder record.EventRecorder) (*leaderelection.LeaderElector, error) {
// 	lockCfg := resourcelock.ResourceLockConfig{
// 		Identity:      identity,
// 		EventRecorder: recorder,
// 	}

// 	leaderElectionLock, err := resourcelock.New(resourcelock.LeasesResourceLock, namespace, name, nil, kubeClient.CoordinationV1(), lockCfg)
// 	if err != nil {
// 		return nil, err
// 	}

// 	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
// 		Lock: leaderElectionLock,
// 		Name: consts.Name,
// 		Callbacks: leaderelection.LeaderCallbacks{
// 			OnStartedLeading: func(_ context.Context) { log.Info("gained leader status") },
// 			OnNewLeader:      func(identity string) { log.WithField("leader", identity).Info("new leader") },
// 			OnStoppedLeading: func() { log.Info("leader status lost") },
// 		},
// 		LeaseDuration: 15 * time.Second,
// 		RenewDeadline: 10 * time.Second,
// 		RetryPeriod:   2 * time.Second,
// 	})
// 	if err != nil {
// 		return nil, err
// 	}

// 	healthzAdapter := leaderelection.NewLeaderHealthzAdaptor(0)
// 	healthzAdapter.SetLeaderElection(elector)

// 	http.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
// 		err := healthzAdapter.Check(request)
// 		if err != nil {
// 			writer.WriteHeader(http.StatusInternalServerError)
// 		} else {
// 			writer.WriteHeader(http.StatusOK)
// 		}
// 	})

// 	go func() {
// 		err := http.ListenAndServe(fmt.Sprintf(":%d", healthPort), nil)
// 		if err != nil {
// 			log.WithError(err).Error("failed to serve /healthz endpoint")
// 		}
// 	}()

// 	go elector.Run(ctx)

// 	return elector, nil
// }

// Creates a new event recorder that forwards to Kubernetes to persist "kind: Event" objects
func eventRecorder(kubeClient kubernetes.Interface) record.EventRecorder {
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&v1.EventSinkImpl{Interface: kubeClient.CoreV1().Events("")})

	source := corev1.EventSource{Component: consts.Name}
	return broadcaster.NewRecorder(scheme.Scheme, source)
}

func parseArgs() *args {
	args := &args{}

	flag.Int32Var(&args.loglevel, "v", int32(log.InfoLevel), "set log level")
	flag.StringVar(&args.kubeconfig, "kubeconfig", "", "path to kubeconfig file")
	// flag.BoolVar(&args.enableLeaderElection, "leader-election", false, "use kubernetes leader election")
	// flag.StringVar(&args.leaderElectionResourceName, "leader-election-resource-name", consts.Name, "name for leader election resource")
	// flag.StringVar(&args.leaderElectionLeaseName, "leader-election-lease-name", "", "name for leader election lease (unique for each pod)")
	// flag.StringVar(&args.leaderElectionNamespace, "leader-election-namespace", "", "namespace for leader election")
	// flag.IntVar(&args.leaderElectionHealthzPort, "leader-election-healtz-port", 8080, "port to use for serving the /healthz endpoint")

	flag.Parse()

	return args
}
