# Cluster API control plane controller for MicroK8s

[Cluster API](https://cluster-api.sigs.k8s.io/) provides declarative APIs to provision, upgrade, and operate Kubernetes clusters.

The [control plane controller in cluster API](https://cluster-api.sigs.k8s.io/user/concepts.html#control-plane) continuously monitors the state of the provisioned cluster and ensures it matches the one desired.

This project offers a cluster API control plane controller that manages the control plane of a [MicroK8s](https://github.com/canonical/microk8s) cluster. It is expected to be used along with the respective [MicroK8s specific machine bootstrap provider](https://github.com/canonical/cluster-api-bootstrap-provider-microk8s).

Please see the [bootstrap provider repository](https://github.com/canonical/cluster-api-bootstrap-provider-microk8s) on how to get started with MicroK8s on CLuster API.