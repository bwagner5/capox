/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrastructurev1alpha1 "github.com/oxidecomputer/cluster-api-provider-oxide/api/v1alpha1"
	"github.com/oxidecomputer/cluster-api-provider-oxide/internal/cloud"
	"github.com/oxidecomputer/oxide.go/oxide"
)

// OxideClusterReconciler reconciles a OxideCluster object
type OxideClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// TODO: Audit RBAC permissions.
//
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxideclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxideclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxideclusters/finalizers,verbs=update
//
// +kubebuilder:rbac:groups=infrastructure.machine.x-k8s.io,resources=oxidemachines,verbs=get;list;watch
//
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=clusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=cluster.x-k8s.io,resources=machines,verbs=get;list;watch
//
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *OxideClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var oxideCluster infrastructurev1alpha1.OxideCluster
	if err := r.Get(ctx, req.NamespacedName, &oxideCluster); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	patchHelper, err := patch.NewHelper(&oxideCluster, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building patch helper: %w", err)
	}

	cluster, err := util.GetOwnerCluster(ctx, r.Client, oxideCluster.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		log.Info("missing ownerRef on OxideCluster", "name", oxideCluster.Name)
	}

	oxideClient, err := cloud.NewOxideClient(ctx, r.Client, oxideCluster)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile Oxide floating IP, to be attached to an arbitrary instance and used as the control plane endpoint host.
	//
	// TODO: Use a load balancer instead, once Oxide has native load balancing support.
	var ip *oxide.FloatingIp
	ipName := fmt.Sprintf("k8s-cluster-api-endpoint-%s-%s", oxideCluster.Namespace, oxideCluster.Name)
	if oxideCluster.Spec.ControlPlaneEndpoint.Host == "" {
		ip, err = oxideClient.FloatingIpCreate(ctx, oxide.FloatingIpCreateParams{
			Project: oxide.NameOrId(oxideCluster.Spec.Project),
			Body: &oxide.FloatingIpCreate{
				Name: oxide.Name(ipName),
				AddressAllocator: oxide.AddressAllocator{
					Value: oxide.AddressAllocatorAuto{
						PoolSelector: oxide.PoolSelector{
							Value: oxide.PoolSelectorExplicit{
								Pool: oxide.NameOrId(oxideCluster.Spec.IPPool),
							},
						},
					},
				},
			},
		})
		if err != nil {
			var httpError *oxide.HTTPError
			if errors.As(err, &httpError) && httpError.ErrorResponse.ErrorCode == "ObjectAlreadyExists" {
				log.Info("floating ip already exists", "name", ipName)
			} else {
				return ctrl.Result{}, err
			}
		}
		oxideCluster.Spec.ControlPlaneEndpoint.Host = ip.Ip
		oxideCluster.Spec.ControlPlaneEndpoint.Port = 6443
	} else {
		ip, err = oxideClient.FloatingIpView(ctx, oxide.FloatingIpViewParams{
			Project:    oxide.NameOrId(oxideCluster.Spec.Project),
			FloatingIp: oxide.NameOrId(ipName),
		})
		if err != nil {
			return ctrl.Result{}, err
		}
	}
	oxideCluster.Status.Initialization.Provisioned = ptr.To(true)

	// Ensure floating IP is attached to an instance. Use the 0th ready machine if unattached.
	if ip.InstanceId == "" {
		var machines infrastructurev1alpha1.OxideMachineList
		if err := r.List(ctx, &machines, client.InNamespace(oxideCluster.Namespace), client.MatchingLabels{
			clusterv1.ClusterNameLabel:         oxideCluster.Name,
			clusterv1.MachineControlPlaneLabel: "",
		}); err != nil {
			return ctrl.Result{}, err
		}
		for _, machine := range machines.Items {
			instanceID, err := cloud.InstanceIDFromProviderID(machine.Spec.ProviderID)
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("parsing provider id: %w", err)
			}
			if machine.Status.Initialization.Provisioned != nil && *machine.Status.Initialization.Provisioned {
				if _, err := oxideClient.FloatingIpAttach(ctx, oxide.FloatingIpAttachParams{
					FloatingIp: oxide.NameOrId(ip.Id),
					Body: &oxide.FloatingIpAttach{
						Kind:   oxide.FloatingIpParentKindInstance,
						Parent: oxide.NameOrId(instanceID),
					},
				}); err != nil {
					return ctrl.Result{}, err
				}
				break
			}
		}
	}

	if err := patchHelper.Patch(ctx, &oxideCluster); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching cluster: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OxideClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.OxideCluster{}).
		Watches(&infrastructurev1alpha1.OxideMachine{}, handler.EnqueueRequestsFromMapFunc(
			r.oxideMachineToOxideCluster,
		)).
		Named("oxidecluster").
		Complete(r)
}

// oxideMachineToOxideCluster watches for machine events and requeues the cluster for update if needed. Used to ensure that the floating IP is always attached to an instance.
func (r *OxideClusterReconciler) oxideMachineToOxideCluster(ctx context.Context, obj client.Object) []reconcile.Request {
	clusterName, ok := obj.GetLabels()[clusterv1.ClusterNameLabel]
	if !ok {
		return nil
	}
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Namespace: obj.GetNamespace(),
			Name:      clusterName,
		}},
	}

}
