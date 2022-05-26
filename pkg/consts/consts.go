package consts

import "time"

var (
	Version = "0.0.0-unknown"
	Name    = "appsolo-controller"
)

const (
	// These defaults line up with Kubernetes' "node.kubernetes.io/unreachable: NoExecute" label so that a removed
	// Pod will not be scheduled on an unreachable node
	DefaultNewResourceGracePeriod   = 45 * time.Second
	DefaultKnownResourceGracePeriod = 45 * time.Second

	DefaultReconcileInterval = 10 * time.Second
)
