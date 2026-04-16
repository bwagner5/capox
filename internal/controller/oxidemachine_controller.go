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
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	infrastructurev1alpha1 "github.com/oxidecomputer/cluster-api-provider-oxide/api/v1alpha1"
	"github.com/oxidecomputer/cluster-api-provider-oxide/internal/cloud"
	"github.com/oxidecomputer/oxide.go/oxide"
)

// OxideMachineReconciler reconciles a OxideMachine object
type OxideMachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxidemachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxidemachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=oxidemachines/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the OxideMachine object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *OxideMachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var oxideMachine infrastructurev1alpha1.OxideMachine
	if err := r.Get(ctx, req.NamespacedName, &oxideMachine); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	patchHelper, err := patch.NewHelper(&oxideMachine, r.Client)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("building patch helper: %w", err)
	}

	machine, err := util.GetOwnerMachine(ctx, r.Client, oxideMachine.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if machine == nil {
		log.Info("waiting for owner machine reference")
		return ctrl.Result{}, nil
	}

	clusterName := machine.Labels[clusterv1.ClusterNameLabel]

	var oxideCluster infrastructurev1alpha1.OxideCluster
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: machine.Namespace,
		Name:      clusterName,
	}, &oxideCluster); err != nil {
		return ctrl.Result{}, err
	}

	oxideClient, err := cloud.NewOxideClient(ctx, r.Client, oxideCluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("constructing oxide client: %w", err)
	}

	projectName := oxideCluster.Spec.Project
	instanceName := fmt.Sprintf("capi-instance-%s-%s-%s", oxideMachine.Namespace, oxideCluster.Name, oxideMachine.Name)

	// Ensure instance exists. Instance creation idempotently creates the disk and NIC as well, so create all resources in a single request.
	var instance *oxide.Instance
	if oxideMachine.Spec.ProviderID == "" {
		diskName := fmt.Sprintf("capi-boot-disk-%s-%s-%s", oxideMachine.Namespace, oxideCluster.Name, oxideMachine.Name)
		nicName := fmt.Sprintf("capi-nic-%s-%s-%s", oxideMachine.Namespace, oxideCluster.Name, oxideMachine.Name)

		bootstrapSecretName := machine.Spec.Bootstrap.DataSecretName
		if bootstrapSecretName == nil {
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		var bootstrapSecret corev1.Secret
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: machine.Namespace,
			Name:      *bootstrapSecretName,
		}, &bootstrapSecret); err != nil {
			return ctrl.Result{}, fmt.Errorf("fetching bootstrap secret: %w")
		}
		if _, ok := bootstrapSecret.Data["value"]; !ok {
			return ctrl.Result{}, fmt.Errorf("missing `value` key in bootstrap secret %s", bootstrapSecretName)
		}

		instance, err = oxideClient.InstanceCreate(ctx, oxide.InstanceCreateParams{
			Project: oxide.NameOrId(projectName),
			Body: &oxide.InstanceCreate{
				Name:     oxide.Name(instanceName),
				Hostname: oxide.Hostname(instanceName),
				Ncpus:    oxide.InstanceCpuCount(oxideMachine.Spec.NCpus),
				Memory:   oxide.ByteCount(oxideMachine.Spec.Memory.Value()),
				Start:    ptr.To(true),
				UserData: base64.StdEncoding.EncodeToString(bootstrapSecret.Data["value"]),
				BootDisk: oxide.InstanceDiskAttachment{
					Value: oxide.InstanceDiskAttachmentCreate{
						Name: oxide.Name(diskName),
						Size: oxide.ByteCount(oxideMachine.Spec.DiskSize.Value()),
						DiskBackend: oxide.DiskBackend{
							Value: oxide.DiskBackendDistributed{
								DiskSource: oxide.DiskSource{
									Value: oxide.DiskSourceImage{
										ImageId: oxideMachine.Spec.ImageID,
									},
								},
							},
						},
					},
				},
				NetworkInterfaces: oxide.InstanceNetworkInterfaceAttachment{
					Value: oxide.InstanceNetworkInterfaceAttachmentCreate{
						Params: []oxide.InstanceNetworkInterfaceCreate{
							{
								Name:       oxide.Name(nicName),
								VpcName:    oxide.Name(oxideCluster.Spec.VPC),
								SubnetName: oxide.Name(oxideCluster.Spec.Subnet),
							},
						},
					},
				},
			},
		})
		if err != nil {
			// Look up the instance if creation failed with a conflict, and adopt the existing instance if found. Note: if an instance was created out of band with unexpected parameters, it will be adopted as well; operators shouldn't create or modify these instances outside the reconciler.
			var httpErr *oxide.HTTPError
			if errors.As(err, &httpErr) && httpErr.ErrorResponse.ErrorCode == "ObjectAlreadyExists" {
				instance, err = oxideClient.InstanceView(ctx, oxide.InstanceViewParams{Project: oxide.NameOrId(projectName), Instance: oxide.NameOrId(instanceName)})
				if err != nil {
					return ctrl.Result{}, fmt.Errorf("viewing existing oxide instance: %w", err)
				}
			} else {
				return ctrl.Result{}, fmt.Errorf("creating oxide instance: %w", err)
			}
		}

		// Patch the resource and start a new helper after setting the provider ID. If the created instance isn't running, we won't reach the Patch() at the end of the reconciler.
		oxideMachine.Spec.ProviderID = cloud.NewProviderID(instance.Id)
		if err := patchHelper.Patch(ctx, &oxideMachine); err != nil {
			return ctrl.Result{}, fmt.Errorf("patching machine: %w", err)
		}
		patchHelper, err = patch.NewHelper(&oxideMachine, r.Client)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("building patch helper: %w", err)
		}
	} else {
		instance, err = oxideClient.InstanceView(ctx, oxide.InstanceViewParams{
			Project:  oxide.NameOrId(projectName),
			Instance: oxide.NameOrId(instanceName),
		})
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("fetching oxide instance: %w", err)
		}
	}

	// Ensure instance is running.
	switch instance.RunState {
	case oxide.InstanceStateStopped:
		if _, err := oxideClient.InstanceStart(ctx, oxide.InstanceStartParams{
			Project:  oxide.NameOrId(oxideCluster.Spec.Project),
			Instance: oxide.NameOrId(instance.Id),
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("starting instance: %w", err)
		}
		log.Info("waiting for instance to start", "state", instance.RunState)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case oxide.InstanceStateCreating, oxide.InstanceStateStarting:
		log.Info("waiting for instance to start", "state", instance.RunState)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	case oxide.InstanceStateRunning:
		log.Info("instance is running; marking as provisioned")
		oxideMachine.Status.Initialization.Provisioned = ptr.To(true)
	}

	if err := patchHelper.Patch(ctx, &oxideMachine); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching machine: %w", err)
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *OxideMachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrastructurev1alpha1.OxideMachine{}).
		Named("oxidemachine").
		Complete(r)
}
