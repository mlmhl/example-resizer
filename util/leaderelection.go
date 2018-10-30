package util

import (
	"context"
	"time"

	"github.com/golang/glog"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
)

type LeaderElectionConfig struct {
	Identity      string
	LockName      string
	Namespace     string
	RetryPeriod   time.Duration
	LeaseDuration time.Duration
	RenewDeadLine time.Duration
}

func NewLeaderLock(
	kubeClient kubernetes.Interface,
	eventRecorder record.EventRecorder,
	config *LeaderElectionConfig) (resourcelock.Interface, error) {
	return resourcelock.New(resourcelock.EndpointsResourceLock,
		config.Namespace,
		config.LockName,
		kubeClient.CoreV1(),
		resourcelock.ResourceLockConfig{
			Identity:      config.Identity,
			EventRecorder: eventRecorder,
		})
}

func RunAsLeader(lock resourcelock.Interface, config *LeaderElectionConfig, startFunc func(context.Context)) {
	leaderelection.RunOrDie(context.TODO(), leaderelection.LeaderElectionConfig{
		Lock:          lock,
		RetryPeriod:   config.RetryPeriod,
		LeaseDuration: config.LeaseDuration,
		RenewDeadline: config.RenewDeadLine,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func( ctx context.Context) {
				glog.V(3).Info("Became leader, starting")
				startFunc(ctx)
			},
			OnStoppedLeading: func() {
				glog.Fatal("Stopped leading")
			},
			OnNewLeader: func(identity string) {
				glog.V(3).Infof("Current leader: %s", identity)
			},
		},
	})
}
