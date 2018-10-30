package resizer

import (
	"io/ioutil"
	"path/filepath"
	"strconv"

	"github.com/mlmhl/external-resizer/controller"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"github.com/mlmhl/external-resizer/util"
)

const sizeFileName = "kubernetes-host-path-size"

// This Resizer is meant for development and testing only and WILL NOT WORK in a multi-node cluster.
// Will create a size file under host path to indicate the latest size.
func New() controller.Resizer {
	return hostPathResizer{}
}

func Name() string {
	return util.SanitizeName("kubernetes.io/host-path")
}

type hostPathResizer struct{}

func (h hostPathResizer) CanSupport(pv *v1.PersistentVolume) bool {
	hostPath := pv.Spec.HostPath
	return hostPath != nil && isDirectoryHostPath(hostPath.Type)
}

func isDirectoryHostPath(typ *v1.HostPathType) bool {
	return typ == nil || *typ == "" || *typ == v1.HostPathDirectory || *typ == v1.HostPathDirectoryOrCreate
}

func (h hostPathResizer) Resize(
	pv *v1.PersistentVolume,
	requestSize resource.Quantity) (resource.Quantity, bool, error) {
	oldSize := pv.Spec.Capacity[v1.ResourceStorage]
	err := ioutil.WriteFile(filepath.Join(pv.Spec.HostPath.Path, sizeFileName),
		[]byte(strconv.FormatInt(requestSize.Value(), 10)), 0644)
	if err != nil {
		return oldSize, false, err
	}
	return requestSize, false, nil
}
