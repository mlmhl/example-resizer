package util

import (
	"encoding/json"
	"fmt"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
)

var knownResizeConditions = map[v1.PersistentVolumeClaimConditionType]bool{
	v1.PersistentVolumeClaimResizing:                true,
	v1.PersistentVolumeClaimFileSystemResizePending: true,
}

func PVCKey(pvc *v1.PersistentVolumeClaim) string {
	return fmt.Sprintf("%s/%s", pvc.Namespace, pvc.Name)
}

func MergeResizeConditionsOfPVC(oldConditions, newConditions []v1.PersistentVolumeClaimCondition) []v1.PersistentVolumeClaimCondition {
	newConditionSet := make(map[v1.PersistentVolumeClaimConditionType]v1.PersistentVolumeClaimCondition, len(newConditions))
	for _, condition := range newConditions {
		newConditionSet[condition.Type] = condition
	}

	var resultConditions []v1.PersistentVolumeClaimCondition
	for _, condition := range oldConditions {
		// If Condition is of not resize type, we keep it.
		if _, ok := knownResizeConditions[condition.Type]; !ok {
			newConditions = append(newConditions, condition)
			continue
		}
		if newCondition, ok := newConditionSet[condition.Type]; ok {
			// Use the new condition to replace old condition with same type.
			resultConditions = append(resultConditions, newCondition)
			delete(newConditionSet, condition.Type)
		}

		// Drop old conditions whose type not exist in new conditions.
	}

	// Append remains resize conditions.
	for _, condition := range newConditionSet {
		resultConditions = append(resultConditions, condition)
	}

	return resultConditions
}

// PatchPVCStatus updates PVC status using PATCH verb
func PatchPVCStatus(
	oldPVC *v1.PersistentVolumeClaim,
	newPVC *v1.PersistentVolumeClaim,
	kubeClient kubernetes.Interface) (*v1.PersistentVolumeClaim, error) {
	patchBytes, err := getPatchData(oldPVC, newPVC)
	if err != nil {
		return nil, fmt.Errorf("can't patch status of PVC %s as generate path data failed: %v", PVCKey(oldPVC), err)
	}
	updatedClaim, updateErr := kubeClient.CoreV1().PersistentVolumeClaims(oldPVC.Namespace).
		Patch(oldPVC.Name, types.StrategicMergePatchType, patchBytes, "status")
	if updateErr != nil {
		return nil, fmt.Errorf("can't patch status of  PVC %s with %v", PVCKey(oldPVC), updateErr)
	}
	return updatedClaim, nil
}

func UpdatePVCapacity(pv *v1.PersistentVolume, newCapacity resource.Quantity, kubeClient kubernetes.Interface) error {
	newPV := pv.DeepCopy()
	newPV.Spec.Capacity[v1.ResourceStorage] = newCapacity
	patchBytes, err := getPatchData(pv, newPV)
	if err != nil {
		return fmt.Errorf("can't update capacity of PV %s as generate path data failed: %v", pv.Name, err)
	}
	_, updateErr := kubeClient.CoreV1().PersistentVolumes().Patch(pv.Name, types.StrategicMergePatchType, patchBytes)
	if updateErr != nil {
		return fmt.Errorf("update capacity of PV %s failed: %v", pv.Name, updateErr)
	}
	return nil
}

func getPatchData(oldObj, newObj interface{}) ([]byte, error) {
	oldData, err := json.Marshal(oldObj)
	if err != nil {
		return nil, fmt.Errorf("marshal old object failed: %v", err)
	}
	newData, err := json.Marshal(newObj)
	if err != nil {
		return nil, fmt.Errorf("mashal new object failed: %v", err)
	}
	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, oldObj)
	if err != nil {
		return nil, fmt.Errorf("CreateTwoWayMergePatch failed: %v", err)
	}
	return patchBytes, nil
}
