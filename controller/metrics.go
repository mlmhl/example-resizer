package controller

import (
	"net/http"
	"time"

	"github.com/mlmhl/external-resizer/util"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/api/core/v1"
)

const (
	subsystem         = "resize_controller" // Prometheus subsystem name for resize controller.
	namespaceLabel    = "namespace"         // Prometheus label name for k8s namespace.
	storageClassLabel = "storage_class"     // Prometheus label name for k8s storage class.
)

var (
	// pvcResizeTotal is used to collect accumulated count of persistent volume claims resized.
	pvcResizeTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "pvc_resize_total",
			Help:      "Total number of persistent volume claims resized, broken down by namespace and storage class name.",
		}, []string{namespaceLabel, storageClassLabel})
	// pvcResizeFailed is used to collect accumulated count of persistent volume claim resize failed attempts.
	pvcResizeFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "pvc_resize_failed",
			Help:      "Total number of persistent volume claim resize failed attempts, broken down by namespace and storage class name.",
		}, []string{namespaceLabel, storageClassLabel})
	pvcResizeDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Subsystem: subsystem,
			Name:      "pvc_resize_duration_seconds",
			Help:      "Latency in seconds to resize persistent volume claims. Broken down by namespace and storage class name.",
		}, []string{namespaceLabel, storageClassLabel})
)

type MetricConfig struct {
	Path    string
	Address string
}

func startMetricsServer(config *MetricConfig) {
	prometheus.MustRegister(pvcResizeTotal, pvcResizeFailed, pvcResizeDurationSeconds)
	http.Handle(config.Path, promhttp.Handler())
	err := http.ListenAndServe(config.Address, nil)
	if err != nil {
		glog.Fatalf("Failed to start metrics server : %v", err)
	}
}

func resizeFuncWithMetrics(resizeFunc resizeFunc) resizeFunc {
	return func(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) error {
		startTime := time.Now()
		err := resizeFunc(pvc, pv)
		if err != nil {
			pvcResizeFailed.WithLabelValues(pvc.Namespace, util.GetPVCStorageClass(pvc)).Inc()
		}
		pvcResizeTotal.WithLabelValues(pvc.Namespace, util.GetPVCStorageClass(pvc)).Inc()
		pvcResizeDurationSeconds.WithLabelValues(pvc.Namespace, util.GetPVCStorageClass(pvc)).
			Observe(time.Since(startTime).Seconds())
		return err
	}
}
