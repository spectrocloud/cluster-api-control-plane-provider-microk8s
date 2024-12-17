package controllers

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sort"
	"strings"
	"time"

	clusterv1beta1 "github.com/canonical/cluster-api-control-plane-provider-microk8s/api/v1beta1"
	"github.com/canonical/cluster-api-control-plane-provider-microk8s/pkg/clusteragent"
	"github.com/canonical/cluster-api-control-plane-provider-microk8s/pkg/images"
	"github.com/canonical/cluster-api-control-plane-provider-microk8s/pkg/token"
	"github.com/go-logr/logr"
	"golang.org/x/mod/semver"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/storage/names"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	"sigs.k8s.io/cluster-api/controllers/external"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultClusterAgentPort string = "25000"
	defaultDqlitePort       string = "19001"
)

type errServiceUnhealthy struct {
	service string
	reason  string
}

func (e *errServiceUnhealthy) Error() string {
	return fmt.Sprintf("Service %s is unhealthy: %s", e.service, e.reason)
}

func (r *MicroK8sControlPlaneReconciler) reconcile(ctx context.Context, cluster *clusterv1.Cluster, tcp *clusterv1beta1.MicroK8sControlPlane) (res ctrl.Result, err error) {
	logger := log.FromContext(ctx)

	logger.Info("reconcile MicroK8sControlPlane")

	// Update ownerrefs on infra templates
	if err := r.reconcileExternalReference(ctx, tcp.Spec.InfrastructureTemplate, cluster); err != nil {
		return ctrl.Result{}, err
	}

	// If ControlPlaneEndpoint is not set, return early
	if !cluster.Spec.ControlPlaneEndpoint.IsValid() {
		logger.Info("cluster does not yet have a ControlPlaneEndpoint defined")
		return ctrl.Result{}, nil
	}

	// TODO: handle proper adoption of Machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		logger.Error(err, "failed to retrieve control plane machines for cluster")
		return ctrl.Result{}, err
	}

	conditionGetters := make([]conditions.Getter, len(ownedMachines))

	for i, v := range ownedMachines {
		conditionGetters[i] = &v
	}

	conditions.SetAggregate(tcp, clusterv1beta1.MachinesReadyCondition,
		conditionGetters, conditions.AddSourceRef(), conditions.WithStepCounterIf(false))

	var (
		errs        error
		result      ctrl.Result
		phaseResult ctrl.Result
	)

	// run all similar reconcile steps in the loop and pick the lowest RetryAfter, aggregate errors and check the requeue flags.
	for _, phase := range []func(context.Context, *clusterv1.Cluster, *clusterv1beta1.MicroK8sControlPlane,
		[]clusterv1.Machine) (ctrl.Result, error){
		r.reconcileNodeHealth,
		r.reconcileConditions,
		r.reconcileMachines,
	} {
		phaseResult, err = phase(ctx, cluster, tcp, ownedMachines)
		if err != nil {
			errs = kerrors.NewAggregate([]error{errs, err})
		}

		result = util.LowestNonZeroResult(result, phaseResult)
	}

	if !result.Requeue {
		conditions.MarkTrue(cluster, clusterv1.ControlPlaneInitializedCondition)
	}

	return result, errs
}

func (r *MicroK8sControlPlaneReconciler) reconcileNodeHealth(ctx context.Context, cluster *clusterv1.Cluster, mcp *clusterv1beta1.MicroK8sControlPlane, machines []clusterv1.Machine) (result ctrl.Result, err error) {
	if err := r.nodesHealthcheck(ctx, mcp, cluster, machines); err != nil {
		reason := clusterv1beta1.ControlPlaneComponentsInspectionFailedReason

		if errors.Is(err, &errServiceUnhealthy{}) {
			reason = clusterv1beta1.ControlPlaneComponentsUnhealthyReason
		}

		conditions.MarkFalse(mcp, clusterv1beta1.ControlPlaneComponentsHealthyCondition, reason,
			clusterv1.ConditionSeverityWarning, err.Error())

		return ctrl.Result{RequeueAfter: 10 * time.Second}, err
	} else {
		conditions.MarkTrue(mcp, clusterv1beta1.ControlPlaneComponentsHealthyCondition)
	}

	return ctrl.Result{}, nil
}

func (r *MicroK8sControlPlaneReconciler) reconcileMachines(ctx context.Context, cluster *clusterv1.Cluster, mcp *clusterv1beta1.MicroK8sControlPlane, machines []clusterv1.Machine) (res ctrl.Result, err error) {

	// If we've made it this far, we can assume that all ownedMachines are up to date
	numMachines := len(machines)
	desiredReplicas := int(*mcp.Spec.Replicas)

	controlPlane := r.newControlPlane(cluster, mcp, machines)

	logger := log.FromContext(ctx).WithValues("desired", desiredReplicas, "existing", numMachines)

	var oldVersionMachines []clusterv1.Machine
	var oldVersion, newVersion string

	if numMachines > 0 {
		var err error
		sort.Sort(SortByCreationTimestamp(machines))
		oldVersion, err = getOldestVersion(machines)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to get oldest version: %w", err)
		}
		newVersion = semver.MajorMinor(mcp.Spec.Version)
	}

	upgradeStrategySelected := mcp.Spec.UpgradeStrategy
	if upgradeStrategySelected == "" {
		upgradeStrategySelected = clusterv1beta1.SmartUpgradeStrategyType
	}

	if oldVersion != "" && semver.Compare(oldVersion, newVersion) != 0 {
		if upgradeStrategySelected == clusterv1beta1.RollingUpgradeStrategyType ||
			(upgradeStrategySelected == clusterv1beta1.SmartUpgradeStrategyType &&
				numMachines >= 3) {

			// Assumption: The newer machines are appended at the end of the
			// machines list, due to list being sorted by creation timestamp.
			// So we take the version at the beginning of the
			// list to be the older version and version at the end to be the
			// newer version. This takes care of the following cases:
			//
			// 1) During initialisation: All machines have same version, so no
			// need to find older machines for scaing down.
			//
			// 2) When normal scaling/no upgrades: Similar to 1st case, during
			// normal scaling, all machines have same version.
			//
			// 3) When version is changed in b/w upgrades: During this case,
			// the latest version of machines will be scaled up and all the
			// older versions will be scaled down.

			oldVersionMachines = append(oldVersionMachines, machines[0])

			// We have a old machine, so we create a new one to increase
			// the number of machines to one more than the desired number.
			// This will create an imbalance of one machine and they will
			// be scaled down to the desired number in the next reconcile.

			if numMachines == desiredReplicas && len(oldVersionMachines) > 0 {
				conditions.MarkFalse(mcp, clusterv1beta1.ResizedCondition, clusterv1beta1.ScalingUpReason, clusterv1.ConditionSeverityWarning,
					"Scaling up control plane to %d replicas (actual %d)", desiredReplicas, numMachines)

				// Create a new machine
				logger.Info("Creating a new node")

				return r.bootControlPlane(ctx, cluster, mcp, controlPlane, false)
			}
		} else if upgradeStrategySelected == clusterv1beta1.InPlaceUpgradeStrategyType ||
			(upgradeStrategySelected == clusterv1beta1.SmartUpgradeStrategyType &&
				numMachines < 3) {

			// Make a client that interacts with the workload cluster.
			kubeclient, err := r.kubeconfigForCluster(ctx, util.ObjectKey(cluster))
			if err != nil {
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			defer kubeclient.Close() //nolint:errcheck

			// For each machine, get the node and upgrade it
			for _, machine := range machines {
				if isMachineUpgraded(machine, newVersion) {
					logger.Info("Machine already upgraded", "machine", machine.Name, "version", newVersion)
					continue
				}

				if machine.Status.NodeRef == nil {
					logger.Info("Machine does not have a nodeRef yet, requeueing...", "machine", machine.Name)
					return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
				}

				// Get the node for the machine
				node, err := kubeclient.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to get node: %w", err)
				}

				logger.Info(fmt.Sprintf("Creating upgrade pod on %s...", node.Name))
				pod, err := createUpgradePod(ctx, kubeclient, node.Name, mcp.Spec.Version)
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to create upgrade pod: %w", err)
				}

				logger.Info("Waiting for node to be updated to the given version...", "node", node.Name)
				if err := waitForNodeUpgrade(ctx, kubeclient, node.Name, mcp.Spec.Version); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to wait for node upgrade: %w", err)
				}

				logger.Info("Node upgraded successfully.", "node", node.Name)
				// Update the machine version
				currentMachine := &clusterv1.Machine{}
				currentMachineName := node.Annotations["cluster.x-k8s.io/machine"]
				if err := r.Client.Get(ctx, client.ObjectKey{Namespace: cluster.Namespace, Name: currentMachineName}, currentMachine); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to get machine: %w", err)
				}

				logger.Info("Updating machine version...", "machine", currentMachine.Name)
				currentMachine.Spec.Version = &mcp.Spec.Version
				logger.Info(fmt.Sprintf("Now updating machine %s version to %s...", currentMachine.Name, *currentMachine.Spec.Version))
				if err := r.Client.Update(ctx, currentMachine); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update machine: %w", err)
				}

				logger.Info(fmt.Sprintf("Removing upgrade pod %s from %s...", pod.ObjectMeta.Name, node.Name))
				if err := waitForPodDeletion(ctx, kubeclient, pod.ObjectMeta.Name); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to wait for pod deletion: %w", err)
				}

				logger.Info(fmt.Sprintf("Upgrade of node %s completed.\n", node.Name))
			}
		}

	}

	switch {
	// We are creating the first replica
	case numMachines < desiredReplicas && numMachines == 0:
		// Create new Machine w/ init
		logger.Info("initializing control plane")

		return r.bootControlPlane(ctx, cluster, mcp, controlPlane, true)
	// We are scaling up
	case numMachines < desiredReplicas && numMachines > 0:
		conditions.MarkFalse(mcp, clusterv1beta1.ResizedCondition, clusterv1beta1.ScalingUpReason, clusterv1.ConditionSeverityWarning,
			"Scaling up control plane to %d replicas (actual %d)", desiredReplicas, numMachines)

		// Create a new Machine w/ join
		logger.Info("scaling up control plane")

		return r.bootControlPlane(ctx, cluster, mcp, controlPlane, false)
	// We are scaling down
	case numMachines > desiredReplicas:
		conditions.MarkFalse(mcp, clusterv1beta1.ResizedCondition, clusterv1beta1.ScalingDownReason, clusterv1.ConditionSeverityWarning,
			"Scaling down control plane to %d replicas (actual %d)",
			desiredReplicas, numMachines)

		if numMachines < 4 && desiredReplicas == 3 {
			conditions.MarkFalse(mcp, clusterv1beta1.ResizedCondition, clusterv1beta1.ScalingDownReason, clusterv1.ConditionSeverityError,
				"Cannot scale down control plane nodes to less than 3 nodes")

			return res, nil
		}

		if err := r.ensureNodesBooted(ctx, controlPlane.MCP, cluster, machines); err != nil {
			logger.Error(err, "waiting for all nodes to finish boot sequence")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		logger.Info("scaling down control plane")

		res, err = r.scaleDownControlPlane(ctx, mcp, util.ObjectKey(cluster), controlPlane.MCP.Name, machines)
		if err != nil {
			if res.Requeue || res.RequeueAfter > 0 {
				logger.Error(err, "failed to scale down control plane")
				return res, nil
			}
		}

		return res, err
	default:
		if !mcp.Status.Bootstrapped {
			if err := r.bootstrapCluster(ctx, mcp, cluster, machines); err != nil {
				conditions.MarkFalse(mcp, clusterv1beta1.MachinesBootstrapped, clusterv1beta1.WaitingForMicroK8sBootReason, clusterv1.ConditionSeverityInfo, err.Error())

				logger.Info("bootstrap failed, retrying in 20 seconds")

				return ctrl.Result{RequeueAfter: time.Second * 20}, nil
			}

			conditions.MarkTrue(mcp, clusterv1beta1.MachinesBootstrapped)

			mcp.Status.Bootstrapped = true
		}

		if conditions.Has(mcp, clusterv1beta1.MachinesReadyCondition) {
			conditions.MarkTrue(mcp, clusterv1beta1.ResizedCondition)
		}

		conditions.MarkTrue(mcp, clusterv1beta1.MachinesCreatedCondition)
	}

	return ctrl.Result{}, nil
}

func (r *MicroK8sControlPlaneReconciler) reconcileExternalReference(ctx context.Context, ref corev1.ObjectReference, cluster *clusterv1.Cluster) error {
	obj, err := external.Get(ctx, r.Client, &ref, cluster.Namespace)
	if err != nil {
		return err
	}

	objPatchHelper, err := patch.NewHelper(obj, r.Client)
	if err != nil {
		return err
	}

	obj.SetOwnerReferences(util.EnsureOwnerRef(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion: clusterv1.GroupVersion.String(),
		Kind:       "Cluster",
		Name:       cluster.Name,
		UID:        cluster.UID,
	}))

	return objPatchHelper.Patch(ctx, obj)
}

func (r *MicroK8sControlPlaneReconciler) bootControlPlane(ctx context.Context, cluster *clusterv1.Cluster, mcp *clusterv1beta1.MicroK8sControlPlane, controlPlane *ControlPlane, first bool) (ctrl.Result, error) {
	// Since the cloned resource should eventually have a controller ref for the Machine, we create an
	// OwnerReference here without the Controller field set
	infraCloneOwner := &metav1.OwnerReference{
		APIVersion: clusterv1beta1.GroupVersion.String(),
		Kind:       "MicroK8sControlPlane",
		Name:       mcp.Name,
		UID:        mcp.UID,
	}

	// Clone the infrastructure template
	infraRef, err := external.CloneTemplate(ctx, &external.CloneTemplateInput{
		Client:      r.Client,
		TemplateRef: &mcp.Spec.InfrastructureTemplate,
		Namespace:   mcp.Namespace,
		OwnerRef:    infraCloneOwner,
		ClusterName: cluster.Name,
		Labels: map[string]string{
			clusterv1.ClusterLabelName:             cluster.Name,
			clusterv1.MachineControlPlaneLabelName: "",
		},
	})
	if err != nil {
		conditions.MarkFalse(mcp, clusterv1beta1.MachinesCreatedCondition,
			clusterv1beta1.InfrastructureTemplateCloningFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, err
	}

	bootstrapConfig := &mcp.Spec.ControlPlaneConfig

	// Clone the bootstrap configuration
	bootstrapRef, err := r.generateMicroK8sConfig(ctx, mcp, cluster, bootstrapConfig)
	if err != nil {
		conditions.MarkFalse(mcp, clusterv1beta1.MachinesCreatedCondition,
			clusterv1beta1.BootstrapTemplateCloningFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, err
	}

	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.SimpleNameGenerator.GenerateName(mcp.Name + "-"),
			Namespace: mcp.Namespace,
			Labels: map[string]string{
				clusterv1.ClusterLabelName:             cluster.Name,
				clusterv1.MachineControlPlaneLabelName: "",
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mcp, clusterv1beta1.GroupVersion.WithKind("MicroK8sControlPlane")),
			},
		},
		Spec: clusterv1.MachineSpec{
			ClusterName:       cluster.Name,
			Version:           &mcp.Spec.Version,
			InfrastructureRef: *infraRef,
			Bootstrap: clusterv1.Bootstrap{
				ConfigRef: bootstrapRef,
			},
			//WARNING: This is a work around, I dont know how this is supposed to be set
		},
	}

	failureDomains := r.getFailureDomain(ctx, cluster)
	if len(failureDomains) > 0 {
		machine.Spec.FailureDomain = &failureDomains[rand.Intn(len(failureDomains))]
	}

	if err := r.Client.Create(ctx, machine); err != nil {
		conditions.MarkFalse(mcp, clusterv1beta1.MachinesCreatedCondition,
			clusterv1beta1.MachineGenerationFailedReason,
			clusterv1.ConditionSeverityError, err.Error())

		return ctrl.Result{}, errors.Wrap(err, "Failed to create machine")
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *MicroK8sControlPlaneReconciler) reconcileConditions(ctx context.Context, cluster *clusterv1.Cluster, tcp *clusterv1beta1.MicroK8sControlPlane, machines []clusterv1.Machine) (result ctrl.Result, err error) {
	if !conditions.Has(tcp, clusterv1beta1.AvailableCondition) {
		conditions.MarkFalse(tcp, clusterv1beta1.AvailableCondition, clusterv1beta1.WaitingForMicroK8sBootReason, clusterv1.ConditionSeverityInfo, "")
	}

	if !conditions.Has(tcp, clusterv1beta1.MachinesBootstrapped) {
		conditions.MarkFalse(tcp, clusterv1beta1.MachinesBootstrapped, clusterv1beta1.WaitingForMachinesReason, clusterv1.ConditionSeverityInfo, "")
	}

	return ctrl.Result{}, nil
}

// getFailureDomain will return a slice of failure domains from the cluster status.
func (r *MicroK8sControlPlaneReconciler) getFailureDomain(ctx context.Context, cluster *clusterv1.Cluster) []string {
	if cluster.Status.FailureDomains == nil {
		return nil
	}

	retList := []string{}
	for key := range cluster.Status.FailureDomains {
		retList = append(retList, key)
	}
	return retList
}

func (r *MicroK8sControlPlaneReconciler) reconcileDelete(ctx context.Context, cluster *clusterv1.Cluster, tcp *clusterv1beta1.MicroK8sControlPlane) (ctrl.Result, error) {
	// Get list of all control plane machines
	ownedMachines, err := r.getControlPlaneMachinesForCluster(ctx, util.ObjectKey(cluster), tcp.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	// If no control plane machines remain, remove the finalizer
	if len(ownedMachines) == 0 {
		controllerutil.RemoveFinalizer(tcp, clusterv1beta1.MicroK8sControlPlaneFinalizer)
		return ctrl.Result{}, r.Client.Update(ctx, tcp)
	}

	for _, ownedMachine := range ownedMachines {
		// Already deleting this machine
		if !ownedMachine.ObjectMeta.DeletionTimestamp.IsZero() {
			continue
		}
		// Submit deletion request
		if err := r.Client.Delete(ctx, &ownedMachine); err != nil && !apierrors.IsNotFound(err) {

			return ctrl.Result{}, err
		}
	}

	// clean up MicroK8s cluster secrets
	for _, secretName := range []string{"kubeconfig", "ca", "jointoken", token.AuthTokenNameSuffix} {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cluster.Namespace,
				Name:      fmt.Sprintf("%s-%s", cluster.Name, secretName),
			},
		}
		if err := r.Client.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			log.FromContext(ctx).Error(err, "failed to delete secret", "secret", secret.Name)
		}
	}

	conditions.MarkFalse(tcp, clusterv1beta1.ResizedCondition, clusterv1.DeletingReason, clusterv1.ConditionSeverityInfo, "")
	// Requeue the deletion so we can check to make sure machines got cleaned up
	return ctrl.Result{RequeueAfter: requeueDuration}, nil
}

func (r *MicroK8sControlPlaneReconciler) bootstrapCluster(ctx context.Context, tcp *clusterv1beta1.MicroK8sControlPlane, cluster *clusterv1.Cluster, machines []clusterv1.Machine) error {

	addresses := []string{}
	for _, machine := range machines {
		found := false

		for _, addr := range machine.Status.Addresses {
			if addr.Type == clusterv1.MachineInternalIP {
				addresses = append(addresses, addr.Address)

				found = true

				break
			}
		}

		if !found {
			return fmt.Errorf("machine %q doesn't have an InternalIP address yet", machine.Name)
		}
	}

	if len(addresses) == 0 {
		return fmt.Errorf("no machine addresses to use for bootstrap")
	}

	return nil
}

func (r *MicroK8sControlPlaneReconciler) scaleDownControlPlane(ctx context.Context, tcp *clusterv1beta1.MicroK8sControlPlane, cluster client.ObjectKey, cpName string, machines []clusterv1.Machine) (ctrl.Result, error) {
	if len(machines) == 0 {
		return ctrl.Result{}, fmt.Errorf("no machines found")
	}

	logger := log.FromContext(ctx)
	logger.WithValues("machines", len(machines)).Info("found control plane machines")

	kubeclient, err := r.kubeconfigForCluster(ctx, cluster)
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	defer kubeclient.Close() //nolint:errcheck

	deleteMachine := machines[len(machines)-1]
	machine := machines[len(machines)-1]
	for i := len(machines) - 1; i >= 0; i-- {
		machine = machines[i]
		logger := logger.WithValues("machineName", machine.Name)

		// do not allow scaling down until all nodes have nodeRefs
		// NOTE(hue): this might happen when we're trying to delete a machine instance that CAN NOT
		// get a nodeRef, e.g. because the infra provider is not able to create the machine.
		// In this case, requeueing here will not solve the issue and only prevents the machine from being deleted.
		if machine.Status.NodeRef == nil {
			logger.Info("machine does not have a nodeRef yet")
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}

		if !machine.ObjectMeta.DeletionTimestamp.IsZero() {
			logger.Info("machine is in process of deletion")

			node, err := kubeclient.CoreV1().Nodes().Get(ctx, machine.Status.NodeRef.Name, metav1.GetOptions{})
			if err != nil {
				// It's possible for the node to already be deleted in the workload cluster, so we just
				// requeue if that's that case instead of throwing a scary error.
				if apierrors.IsNotFound(err) {
					return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
				}
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			// TODO: drain and cordon the node
			logger.WithValues("nodeName", node.Name).Info("deleting node")

			err = kubeclient.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
			if err != nil {
				return ctrl.Result{RequeueAfter: 20 * time.Second}, err
			}

			return ctrl.Result{RequeueAfter: 20 * time.Second}, nil
		}

		// mark the oldest machine to be deleted first
		if machine.CreationTimestamp.Before(&deleteMachine.CreationTimestamp) {
			deleteMachine = machine
		}
	}

	if deleteMachine.Status.NodeRef == nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, fmt.Errorf("%q machine does not have a nodeRef", deleteMachine.Name)
	}

	node := deleteMachine.Status.NodeRef

	logger = logger.WithValues("machineName", deleteMachine.Name, "nodeName", node.Name)

	logger.Info("deleting node from dqlite", "machineName", deleteMachine.Name, "nodeName", node.Name)

	// NOTE(Hue): We do this step as a best effort since this whole logic is implemented to prevent a not-yet-reported bug.
	// The issue is that we were not removing the endpoint from dqlite when we were deleting a machine.
	// This would cause a situation were a joining node failed to join because the endpoint was already in the dqlite cluster.
	// How? The IP assigned to the joining (new) node, previously belonged to a node that was deleted, but the IP is still there in dqlite.
	// If we have 2 machines, deleting one is not safe because it can be the leader and we're not taking care of
	// leadership transfers in the cluster-agent for now. Maybe something for later (TODO)
	// If we have 3 or more machines left, get cluster agent client and delete node from dqlite.
	if len(machines) > 2 {
		portRemap := tcp != nil && tcp.Spec.ControlPlaneConfig.ClusterConfiguration != nil && tcp.Spec.ControlPlaneConfig.ClusterConfiguration.PortCompatibilityRemap

		kubeclient, err := r.kubeconfigForCluster(ctx, cluster)
		if err != nil {
			return ctrl.Result{RequeueAfter: 5 * time.Second}, fmt.Errorf("failed to get kubeconfig for cluster: %w", err)
		}

		defer kubeclient.Close() //nolint:errcheck

		if clusterAgentClient, err := getClusterAgentClient(kubeclient, logger, machines, deleteMachine, portRemap); err == nil {
			if err := r.removeNodeFromDqlite(ctx, clusterAgentClient, cluster, deleteMachine, portRemap); err != nil {
				logger.Error(err, "failed to remove node from dqlite: %w", "machineName", deleteMachine.Name, "nodeName", node.Name)
			}
		} else {
			logger.Error(err, "failed to get cluster agent client")
		}
	}

	logger.Info("deleting machine")

	err = r.Client.Delete(ctx, &deleteMachine)
	if err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("deleting node")
	err = kubeclient.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
	if err != nil {
		return ctrl.Result{RequeueAfter: 20 * time.Second}, err
	}

	// Requeue so that we handle any additional scaling.
	return ctrl.Result{Requeue: true}, nil
}

func getClusterAgentClient(kubeclient *kubernetesClient, logger logr.Logger, machines []clusterv1.Machine, delMachine clusterv1.Machine, portRemap bool) (*clusteragent.Client, error) {
	opts := clusteragent.Options{
		// NOTE(hue): We want to pick a random machine's IP to call POST /dqlite/remove on its cluster agent endpoint.
		// This machine should preferably not be the <delMachine> itself, although this is not forced by Microk8s.
		IgnoreMachineNames: sets.NewString(delMachine.Name),
	}

	port := defaultClusterAgentPort
	if portRemap {
		// https://github.com/canonical/cluster-api-control-plane-provider-microk8s/blob/v0.6.10/control-plane-components.yaml#L96-L102
		port = "30000"
	}

	clusterAgentClient, err := clusteragent.NewClient(kubeclient, logger, machines, port, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize cluster agent client: %w", err)
	}

	return clusterAgentClient, nil
}

// removeMicrok8sNode removes the node from
func (r *MicroK8sControlPlaneReconciler) removeNodeFromDqlite(ctx context.Context, clusterAgentClient *clusteragent.Client,
	clusterKey client.ObjectKey, delMachine clusterv1.Machine, portRemap bool) error {
	dqlitePort := defaultDqlitePort
	if portRemap {
		// https://github.com/canonical/cluster-api-control-plane-provider-microk8s/blob/v0.6.10/control-plane-components.yaml#L96-L102
		dqlitePort = "2379"
	}

	var removeEp string
	for _, addr := range delMachine.Status.Addresses {
		if net.ParseIP(addr.Address) != nil {
			removeEp = fmt.Sprintf("%s:%s", addr.Address, dqlitePort)
			break
		}
	}

	if removeEp == "" {
		return fmt.Errorf("failed to extract endpoint of the deleting machine %q", delMachine.Name)
	}

	token, err := token.Lookup(ctx, r.Client, clusterKey)
	if err != nil {
		return fmt.Errorf("failed to lookup token: %w", err)
	}

	if err := clusterAgentClient.RemoveNodeFromDqlite(ctx, token, removeEp); err != nil {
		return fmt.Errorf("failed to remove node %q from dqlite: %w", removeEp, err)
	}

	return nil
}

// createUpgradePod creates a pod that upgrades the node to the given version.
// If the upgrade pod already exists, it is deleted and a new one will be created.
func createUpgradePod(ctx context.Context, kubeclient *kubernetesClient, nodeName string, nodeVersion string) (*corev1.Pod, error) {
	podName := "upgrade-pod"

	// delete the pod if it exists
	if err := waitForPodDeletion(ctx, kubeclient, podName); err != nil {
		return nil, fmt.Errorf("failed to delete pod %s: %w", podName, err)
	}

	nodeVersion = strings.TrimPrefix(semver.MajorMinor(nodeVersion), "v")

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: corev1.PodSpec{
			NodeName:      nodeName,
			RestartPolicy: corev1.RestartPolicyOnFailure,
			Containers: []corev1.Container{
				{
					Name:  "upgrade",
					Image: images.CurlImage,
					Command: []string{
						"su",
						"-c",
					},
					SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true), RunAsUser: ptr.To(int64(0))},
					Args: []string{
						fmt.Sprintf("curl -X POST -H \"Content-Type: application/json\" --unix-socket /run/snapd.socket -d '{\"action\": \"refresh\",\"channel\":\"%s/stable\"}' http://localhost/v2/snaps/microk8s", nodeVersion),
					},
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      "snapd-socket",
							MountPath: "/run/snapd.socket",
						},
					},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name: "snapd-socket",
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/run/snapd.socket",
						},
					},
				},
			},
		},
	}

	pod, err := kubeclient.CoreV1().Pods("default").Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod %s: %w", podName, err)
	}

	return pod, nil
}

func waitForNodeUpgrade(ctx context.Context, kubeclient *kubernetesClient, nodeName, nodeVersion string) error {
	for attempts := 100; attempts > 0; attempts-- {
		node, err := kubeclient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("failed to get node %s: %w", nodeName, err)
		}
		currentVersion := semver.MajorMinor(node.Status.NodeInfo.KubeletVersion)
		nodeVersion = semver.MajorMinor(nodeVersion)
		if strings.HasPrefix(currentVersion, nodeVersion) {
			return nil
		}

		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("timed out waiting for node %s to be upgraded to version %s", nodeName, nodeVersion)
}

// waitForPodDeletion waits for the pod to be deleted. If the pod doesn't exist, it returns nil.
func waitForPodDeletion(ctx context.Context, kubeclient *kubernetesClient, podName string) error {
	var err error
	for attempts := 5; attempts > 0; attempts-- {
		deleteOptions := metav1.DeleteOptions{
			GracePeriodSeconds: ptr.To(int64(0)),
		}
		err = kubeclient.CoreV1().Pods("default").Delete(ctx, podName, deleteOptions)
		if err == nil || apierrors.IsNotFound(err) {
			return nil
		}
		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("timed out waiting for pod %s to be deleted: %w", podName, err)
}

// getOldestVersion returns the oldest version of the machines.
func getOldestVersion(machines []clusterv1.Machine) (string, error) {
	var v string
	for _, m := range machines {
		if m.Spec.Version == nil {
			// weird!
			continue
		}

		if v == "" {
			v = semver.MajorMinor(*m.Spec.Version)
			continue
		}

		if semver.Compare(v, *m.Spec.Version) > 0 {
			v = semver.MajorMinor(*m.Spec.Version)
		}
	}

	if v == "" {
		return "", fmt.Errorf("no version found")
	}
	return v, nil
}

func isMachineUpgraded(m clusterv1.Machine, newVersion string) bool {
	if m.Spec.Version == nil {
		return false
	}
	machineVersion := semver.MajorMinor(*m.Spec.Version)
	newVersion = semver.MajorMinor(newVersion) // just being extra careful
	return semver.Compare(machineVersion, newVersion) == 0
}
