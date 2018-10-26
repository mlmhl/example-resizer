package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/mlmhl/external-resizer/controller"
	"github.com/mlmhl/external-resizer/examples/hostpath-resizer/pkg/resizer"

	"github.com/golang/glog"
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
		id = fmt.Sprintf("%s-%s", resizer.ResizerName, uuid.NewUUID())
	}

	rc := controller.NewResizeController(id, resizer.New(), kubeClient, *resyncPeriod)
	rc.Run(*workers, wait.NeverStop)
}
