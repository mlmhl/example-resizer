package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/mlmhl/external-resizer/util"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

type ResizeController interface {
	Run(workers int, stopCh <-chan struct{}, metricConfig *MetricConfig, leaderElectionConfig *util.LeaderElectionConfig)
}

type resizeFunc func(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) error

type resizeController struct {
	identity        string
	resizer         Resizer
	kubeClient      kubernetes.Interface
	claimQueue      workqueue.RateLimitingInterface
	eventRecorder   record.EventRecorder
	pvLister        corelisters.PersistentVolumeLister
	pvSynced        cache.InformerSynced
	pvcLister       corelisters.PersistentVolumeClaimLister
	pvcSynced       cache.InformerSynced
	informerFactory informers.SharedInformerFactory

	// Extract the actual resize operation as an interface so that we can add metrics flexible.
	resizeFunc resizeFunc
}

func NewResizeController(
	identity string,
	resizer Resizer,
	kubeClient kubernetes.Interface,
	resyncPeriod time.Duration) ResizeController {
	informerFactory := informers.NewSharedInformerFactory(kubeClient, resyncPeriod)
	pvInformer := informerFactory.Core().V1().PersistentVolumes()
	pvcInformer := informerFactory.Core().V1().PersistentVolumeClaims()

	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&corev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events(v1.NamespaceAll)})
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme,
		v1.EventSource{Component: fmt.Sprintf("external-resizer %s", identity)})

	claimQueue := workqueue.NewNamedRateLimitingQueue(
		workqueue.DefaultControllerRateLimiter(), fmt.Sprintf("%s-pvc", identity))

	ctrl := &resizeController{
		identity:        identity,
		resizer:         resizer,
		kubeClient:      kubeClient,
		pvLister:        pvInformer.Lister(),
		pvSynced:        pvInformer.Informer().HasSynced,
		pvcLister:       pvcInformer.Lister(),
		pvcSynced:       pvcInformer.Informer().HasSynced,
		claimQueue:      claimQueue,
		eventRecorder:   eventRecorder,
		informerFactory: informerFactory,
	}

	// Add a resync period as the PVC's request size can be resized again when we handling
	// a previous resizing request of the same PVC.
	pvcInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPVC,
		UpdateFunc: ctrl.updatePVC,
		DeleteFunc: ctrl.deletePVC,
	}, resyncPeriod)

	return ctrl
}

func (ctrl *resizeController) addPVC(obj interface{}) {
	objKey, err := getPVCKey(obj)
	if err != nil {
		return
	}
	ctrl.claimQueue.Add(objKey)
}

func (ctrl *resizeController) updatePVC(_, newObj interface{}) {
	ctrl.addPVC(newObj)
}

func (ctrl *resizeController) deletePVC(obj interface{}) {
	objKey, err := getPVCKey(obj)
	if err != nil {
		return
	}
	ctrl.claimQueue.Forget(objKey)
}

func getPVCKey(obj interface{}) (string, error) {
	if unknown, ok := obj.(cache.DeletedFinalStateUnknown); ok && unknown.Obj != nil {
		obj = unknown.Obj
	}
	objKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		glog.Errorf("Failed to get key from object: %v", err)
		return "", err
	}
	return objKey, nil
}

func (ctrl *resizeController) Run(
	threadiness int,
	stopCh <-chan struct{},
	metricConfig *MetricConfig,
	leaderElectionConfig *util.LeaderElectionConfig) {
	run := func(_ context.Context) {
		defer ctrl.claimQueue.ShutDown()

		glog.Infof("Starting external resizer %s", ctrl.identity)
		defer glog.Infof("Shutting down external resizer %s", ctrl.identity)

		if metricConfig == nil {
			ctrl.resizeFunc = ctrl.resizePVC
		} else {
			ctrl.resizeFunc = resizeFuncWithMetrics(ctrl.resizePVC)
			go startMetricsServer(metricConfig)
		}

		ctrl.informerFactory.Start(stopCh)
		if !cache.WaitForCacheSync(stopCh, ctrl.pvSynced, ctrl.pvcSynced) {
			glog.Errorf("Cannot sync pv/pvc caches")
			return
		}

		for i := 0; i < threadiness; i++ {
			go wait.Until(ctrl.syncPVCs, 0, stopCh)
		}

		<-stopCh
	}

	if leaderElectionConfig == nil {
		// Leader election disabled.
		run(context.TODO())
	} else {
		lock, err := util.NewLeaderLock(ctrl.kubeClient, ctrl.eventRecorder, leaderElectionConfig)
		if err != nil {
			glog.Fatalf("Error creating leader election lock: %v", err)
		}
		util.RunAsLeader(lock, leaderElectionConfig, run)
	}
}

func (ctrl *resizeController) syncPVCs() {
	key, quit := ctrl.claimQueue.Get()
	if quit {
		return
	}
	defer ctrl.claimQueue.Done(key)

	if err := ctrl.syncPVC(key.(string)); err != nil {
		// Put PVC back to the queue so that we can retry later.
		ctrl.claimQueue.AddRateLimited(key)
	} else {
		ctrl.claimQueue.Forget(key)
	}
}

func (ctrl *resizeController) syncPVC(key string) error {
	glog.V(4).Infof("Started PVC processing %q", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		glog.Errorf("Split meta namespace key of pvc %s failed: %v", key, err)
		return err
	}

	pvc, err := ctrl.pvcLister.PersistentVolumeClaims(namespace).Get(name)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			glog.V(3).Infof("PVC %s/%s is deleted, no need to process it", namespace, name)
			return nil
		}
		glog.Errorf("Get PVC %s/%s failed: %v", namespace, name, err)
		return err
	}

	if !ctrl.pvcNeedResize(pvc) {
		glog.V(4).Infof("No need to resize PVC %q", util.PVCKey(pvc))
		return nil
	}

	pv, err := ctrl.pvLister.Get(pvc.Spec.VolumeName)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			glog.V(3).Infof("PV %s is deleted, no need to process it", pvc.Spec.VolumeName)
			return nil
		}
		glog.Errorf("Get PV %q of pvc %q failed: %v", pvc.Spec.VolumeName, util.PVCKey(pvc), err)
		return err
	}

	if !ctrl.pvNeedResize(pvc, pv) {
		glog.V(4).Infof("No need to resize PV %q", pv.Name)
		return nil
	}

	return ctrl.resizeFunc(pvc, pv)
}

func (ctrl *resizeController) pvcNeedResize(pvc *v1.PersistentVolumeClaim) bool {
	// Only Bound pvc can be expanded.
	if pvc.Status.Phase != v1.ClaimBound {
		return false
	}
	if pvc.Spec.VolumeName == "" {
		return false
	}
	actualSize := pvc.Status.Capacity[v1.ResourceStorage]
	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	return requestSize.Cmp(actualSize) > 0
}

func (ctrl *resizeController) pvNeedResize(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) bool {
	if !ctrl.resizer.CanSupport(pv) {
		glog.V(4).Infof("Resizer %q doesn't support PV %q", ctrl.identity, pv.Name)
		return false
	}

	pvSize := pv.Spec.Capacity[v1.ResourceStorage]
	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	if pvSize.Cmp(requestSize) >= 0 {
		// If PV size is equal or bigger than request size, that means we have already resized PV.
		// In this case we need to check PVC's condition.
		// 1. If PVC in PersistentVolumeClaimResizing condition, we should continue to perform the
		//    resizing operation as we need to know if file system resize if required. (What's more,
		//    we hope the driver can find that the actual size already matched the request size and do nothing).
		// 2. If PVC in PersistentVolumeClaimFileSystemResizePending condition, we need to
		//    do nothing as kubelet will finish file system resizing and mark resize as finished.
		if util.HasFileSystemResizePendingCondition(pvc) {
			// This is case 2.
			return false
		}
		// This is case 1.
		return true
	}

	// PV size is smaller than request size, we need to resize the volume.
	return true
}

// resizePVC will:
// 1. Mark pvc as resizing.
// 2. Resize the pv and volume.
// 3. Mark pvc as resizing finished(no error, no need to resize fs), need resizing fs or resize failed.
func (ctrl *resizeController) resizePVC(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) error {
	if updatedPVC, err := ctrl.markPVCResizeInProgress(pvc); err != nil {
		glog.Errorf("Mark pvc %q as resizing failed: %v", util.PVCKey(pvc), err)
		return err
	} else if updatedPVC != nil {
		pvc = updatedPVC
	}

	// Record an event to indicate that external resizer is resizing this volume.
	ctrl.eventRecorder.Event(pvc, v1.EventTypeNormal, util.VolumeResizing,
		fmt.Sprintf("External resizer is resizing volume %s", pv.Name))

	err := func() error {
		newSize, fsResizeRequired, err := ctrl.resizeVolume(pvc, pv)
		if err != nil {
			return err
		}

		if fsResizeRequired {
			// Resize volume succeeded and need to resize file system by kubelet, mark it as file system resizing required.
			return ctrl.markPVCAsFSResizeRequired(pvc)
		}
		// Resize volume succeeded and no need to resize file system by kubelet, mark it as resizing finished.
		return ctrl.markPVCResizeFinished(pvc, newSize)
	}()

	if err != nil {
		// Record an event to indicate that resize operation is failed.
		ctrl.eventRecorder.Eventf(pvc, v1.EventTypeWarning, util.VolumeResizeFailed, err.Error())
	}

	return err
}

// resizeVolume resize the volume to request size, and update PV's capacity if succeeded.
func (ctrl *resizeController) resizeVolume(
	pvc *v1.PersistentVolumeClaim,
	pv *v1.PersistentVolume) (resource.Quantity, bool, error) {
	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	newSize, fsResizeRequired, err := ctrl.resizer.Resize(pv, requestSize)
	if err != nil {
		glog.Errorf("Resize volume %q by resizer %q failed: %v", pv.Name, ctrl.identity, err)
		return newSize, fsResizeRequired, fmt.Errorf("resize volume %s failed: %v", pv.Name, err)
	}
	glog.V(4).Infof("Resize volume succeeded for volume %q, start to update PV's capacity", pv.Name)

	if err := util.UpdatePVCapacity(pv, newSize, ctrl.kubeClient); err != nil {
		glog.Errorf("Update capacity of PV %q to %s failed: %v", pv.Name, newSize.String(), err)
		return newSize, fsResizeRequired, err
	}
	glog.V(4).Infof("Update capacity of PV %q to %s succeeded", pv.Name, newSize.String())

	return newSize, fsResizeRequired, nil
}

func (ctrl *resizeController) markPVCResizeInProgress(pvc *v1.PersistentVolumeClaim) (*v1.PersistentVolumeClaim, error) {
	// Mark PVC as Resize Started
	progressCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimResizing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions,
		[]v1.PersistentVolumeClaimCondition{progressCondition})
	return util.PatchPVCStatus(pvc, newPVC, ctrl.kubeClient)
}

func (ctrl *resizeController) markPVCResizeFinished(pvc *v1.PersistentVolumeClaim, newSize resource.Quantity) error {
	newPVC := pvc.DeepCopy()
	newPVC.Status.Capacity[v1.ResourceStorage] = newSize
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(pvc.Status.Conditions, []v1.PersistentVolumeClaimCondition{})
	_, err := util.PatchPVCStatus(pvc, newPVC, ctrl.kubeClient)
	if err != nil {
		glog.Errorf("Mark PVC %q as resize finished failed: %v", util.PVCKey(pvc), err)
		return err
	}

	glog.V(4).Infof("Resize PVC %q finished", util.PVCKey(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal, util.VolumeResizeSuccess, "Resize volume succeeded")

	return nil
}

func (ctrl *resizeController) markPVCAsFSResizeRequired(pvc *v1.PersistentVolumeClaim) error {
	pvcCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimFileSystemResizePending,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Waiting for user to (re-)start a pod to finish file system resize of volume on node.",
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions,
		[]v1.PersistentVolumeClaimCondition{pvcCondition})
	_, err := util.PatchPVCStatus(pvc, newPVC, ctrl.kubeClient)
	if err != nil {
		glog.Errorf("Mark PVC %q as file system resize required failed: %v", util.PVCKey(pvc), err)
		return err
	}

	glog.V(4).Infof("Mark PVC %q as file system resize required", util.PVCKey(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal,
		util.FileSystemResizeRequired, "Require file system resize of volume on node")

	return err
}
