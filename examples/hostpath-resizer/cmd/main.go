package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/mlmhl/external-resizer/controller"
	"github.com/mlmhl/external-resizer/examples/hostpath-resizer/pkg/resizer"

	"github.com/golang/glog"
	"github.com/mlmhl/external-resizer/util"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	master       = flag.String("master", "", "Master URL")
	identity     = flag.String("identity", "", "Unique resizer identity")
	kubeConfig   = flag.String("kubeconfig", "", "Absolute path to the kubeconfig")
	resyncPeriod = flag.Duration("resync-period", time.Minute*2, "Resync period for cache")
	workers      = flag.Int("workers", 10, "Concurrency to process multi resize requests")

	enableLeaderElection      = flag.Bool("leader-election", false, "Enable leader election.")
	leaderElectionNamespace   = flag.String("leader-election-namespace", "kube-system", "Namespace where this resizer runs.")
	leaderElectionRetryPeriod = flag.Duration("leader-election-retry-period", time.Second*5,
		"The duration the clients should wait between attempting acquisition and renewal "+
			"of a leadership. This is only applicable if leader election is enabled.")
	leaderElectionLeaseDuration = flag.Duration("leader-election-lease-duration", time.Second*15,
		"The duration that non-leader candidates will wait after observing a leadership "+
			"renewal until attempting to acquire leadership of a led but unrenewed leader "+
			"slot. This is effectively the maximum duration that a leader can be stopped "+
			"before it is replaced by another candidate. This is only applicable if leader "+
			"election is enabled.")
	leaderElectionRenewDeadLine = flag.Duration("leader-election-renew-deadline", time.Second*10,
		"The duration that non-leader candidates will wait after observing a leadership "+
			"renewal until attempting to acquire leadership of a led but unrenewed leader "+
			"slot. This is effectively the maximum duration that a leader can be stopped "+
			"before it is replaced by another candidate. This is only applicable if leader "+
			"election is enabled.")
)

func main() {
	flag.Parse()

	var config *rest.Config
	var err error
	if *master != "" || *kubeConfig != "" {
		config, err = clientcmd.BuildConfigFromFlags(*master, *kubeConfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		glog.Fatalf("Failed to create config: %v", err)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}

	id := *identity
	if len(id) == 0 {
		id = fmt.Sprintf("%s-%s", resizer.Name(), uuid.NewUUID())
	}

	var leaderElectionConfig *util.LeaderElectionConfig
	if *enableLeaderElection {
		leaderElectionConfig = &util.LeaderElectionConfig{
			Identity:      id,
			LockName:      resizer.Name(),
			Namespace:     *leaderElectionNamespace,
			RetryPeriod:   *leaderElectionRetryPeriod,
			LeaseDuration: *leaderElectionLeaseDuration,
			RenewDeadLine: *leaderElectionRenewDeadLine,
		}
	}

	rc := controller.NewResizeController(id, resizer.New(), kubeClient, *resyncPeriod)
	rc.Run(*workers, wait.NeverStop, leaderElectionConfig)
}
