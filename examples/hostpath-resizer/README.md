# HostPath Volume Resizer

`hostpath-resizer` is an out-of-tree resize controller for kubernetes.
This Resizer is meant for development and testing only and WILL NOT WORK in a multi-node cluster.

## Deployment

Compile the resizer.

```console
make build
```

## Test instruction

* Start Kubernetes local cluster

See https://kubernetes.io/.

Please remember to set kube-controller-manager's flag `enable-hostpath-provisioner` to `true` so that you can create a dynamic provisioned HostPath volume.
If you started your cluster by `hack/local-up-cluster.sh`, you can just `export ENABLE_HOSTPATH_PROVISIONER=true`.

* Create a HostPath volume

Create a `StorageClass` first:

```bash
kubectl create -f deploy/sc_hostpath.yaml
```

Then create a HostPath volume:

```bash
kubectl create -f deploy/pvc_hostpath.yaml
```

The PVC should be created successfully.

```console
NAME     STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
pvc-hp   Bound    pvc-2c5c5954-d8ec-11e8-8c72-6c0b84a7cf5b   1Gi        RWO            hostpath       6m27s
```

* Resize the created volume

You can just edit the PVC object and set the requested size to a bigger one.

```bash
kubectl edit pvc pvc-hp
```