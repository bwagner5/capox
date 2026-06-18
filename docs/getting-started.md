

## Build the Oxide images

The CAPI provider requires an image to exist on the rack with the relevant Kubernetes binaries. We use [image-builder](https://github.com/kubernetes-sigs/image-builder) to build these images with Packer and Ansible.

Choose an Oxide project in which to build the image, as well as a base image. Only Ubuntu 24.04 is currently supported. If you don't have an Ubuntu image in your Oxide silo, upload one following the [Oxide image guide](https://docs.oxide.computer/guides/creating-and-sharing-images). Then export environment variables indicating your project and base image disk ID:

    $ export OXIDE_PROJECT=<my-project>
    $ export OXIDE_BOOT_DISK_IMAGE_ID=<my-base-image-id>

You'll also need to export environment variables for Packer to authenticate to the Oxide API:

    $ export OXIDE_HOST=<my-oxide-host>
    $ export OXIDE_TOKEN=<my-oxide-token>

or

    $ export OXIDE_PROFILE=<my-profile>


Clone the image builder:

    $ git clone https://github.com/kubernetes-sigs/image-builder.git

Change directory to images/capi within the image builder repository:

    $ cd image-builder/images/capi

Build an Oxide image:

    $ make build-oxide-ubuntu-2404

## Configure the management cluster

CAPI uses a management cluster to configure and deploy workload clusters. Any Kubernetes cluster can serve as the management cluster, but we'll use [kind](https://kind.sigs.k8s.io/) here:

    $ export MANAGEMENT_CLUSTER=capi-management
    $ kind create cluster --name $MANAGEMENT_CLUSTER
    $ kubectl config use-context kind-$MANAGEMENT_CLUSTER

Add the Oxide credentials to the management cluster to allow the Oxide CAPI provider to call the Oxide API. 
Ensure that the `$OXIDE_HOST` and `$OXIDE_TOKEN` environment variables are set and run:

    $ kubectl create secret generic oxide-credentials \
        --from-literal=oxide-host=${OXIDE_HOST} \
        --from-literal=oxide-token=${OXIDE_TOKEN}

If developing the Oxide CAPI provider locally, build and upload the controller Docker image:

    $ export IMG=ghcr.io/oxidecomputer/cluster-api-provider-oxide:dev
    $ make docker-build
    $ kind load docker-image $IMG --name $MANAGEMENT_CLUSTER

Next, install the `clusterctl` tool by following the [installation docs](https://cluster-api.sigs.k8s.io/user/quick-start.html#install-clusterctl). Use the `clusterctl` CLI to install the core CAPI Kubernetes resources and controllers to the management cluster. 

    $ clusterctl init

Install the Oxide CAPI provider resources and controllers to the management cluster:

    $ make deploy

## Create a workload cluster


Add a firewall rule to the VPC to allow inbound TCP/6443 traffic to the floating IP. 


Export the ID of the image built using `image-builder` above:

    $ export OXIDE_IMAGE_ID=<my-image-id>

A CAPI workload cluster comprises several different resource types. We'll generate a manifest describing all the necessary resources using `clusterctl` to render the provided template:

    $ clusterctl generate cluster \
        quickstart \
        --from templates/cluster-template.yaml \
        --kubernetes-version <kubernetes-version> \
        --control-plane-machine-count 3 \
        --worker-machine-count 3 \
        > cluster-quickstart.yaml

Apply the rendered quickstart manifest to the management cluster:

    $ kubectl apply -f cluster-quickstart.yaml

In order to configure the workload cluster, we'll need to fetch its kubeconfig:

    $ kubectl get secret quickstart-kubeconfig -o jsonpath='{.data.value}' | base64 -d > /tmp/quickstart

In order for Kubernetes nodes in the workload cluster to become healthy, we need to run a CNI plugin (so that nodes can pass their readiness check).  We'll install the Calico CNI:

    $ KUBECONFIG=/tmp/quickstart kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.28.0/manifests/calico.yaml

Next we need the [Oxide Cloud Controller Manager](https://github.com/oxidecomputer/oxide-cloud-controller-manager) (CCM; to set the node's `providerID`).

The CCM requires a Kubernetes secret with credentials to authenticate to the Oxide API, so we'll create that secret first:

    $ KUBECONFIG=/tmp/quickstart kubectl create secret -n kube-system generic quickstart-oxide-cloud-controller-manager \
        --from-literal=oxide-host=<oxide-host> \
        --from-literal=oxide-token=<oxide-token> \
        --from-literal=oxide-project=<oxide-project>

    $ KUBECONFIG=/tmp/quickstart helm install quickstart \
        oci://ghcr.io/oxidecomputer/helm-charts/oxide-cloud-controller-manager \
        --namespace kube-system
