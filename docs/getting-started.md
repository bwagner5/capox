

## Build the Oxide images

The CAPI provider requires an image to exist on the rack with the relevant Kubernetes binaries. We use [image-builder](https://github.com/kubernetes-sigs/image-builder) to build these images with Packer and Ansible.

Choose an Oxide project in which to build the image, as well as a base image. Only Ubuntu 24.04 is currently supported. If you don't have an Ubuntu image in your Oxide silo, upload one following the [Oxide image guide](https://docs.oxide.computer/guides/creating-and-sharing-images). Then export environment variables indicating your project and base image disk ID:

    $ export OXIDE_PROJECT=<my-project>
    $ export OXIDE_BOOT_DISK_IMAGE_ID=<my-base-image-id>

Clone the image builder:

    $ git clone https://github.com/oxidecomputer/image-builder.git

Change directory to images/capi within the image builder repository:

    $ cd image-builder/images/capi

Build an Oxide image:

    $ make build-oxide-ubuntu-2404

## Configure the management cluster

CAPI uses a management cluster to configure and deploy workload clusters. Any Kubernetes cluster can serve as the management cluster, but we'll use [kind](https://kind.sigs.k8s.io/) here:

    $ export MANAGEMENT_CLUSTER=capi-management
    $ kind create cluster --nam $MANAGEMENT_CLUSTER

If developing the Oxide CAPI provider locally, build and upload the controller Docker image:

    $ export IMG=ghcr.io/oxidecomputer/cluster-api-provider-oxide:dev
    $ make docker-build
    $ load docker-image $IMG --name $MANAGEMENT_CLUSTER

Install the core CAPI Kubernetes resources and controllers to the management cluster:

    $ clusterctl init

Install the Oxide CAPI provider resources and controllers to the management cluster:

    $ make deploy

## Create a workload cluster

Export the ID of the image built using `image-builder` above:

    $ export OXIDE_IMAGE_ID=<my-image-id>

A CAPI workload cluster comprises several different resource types. We'll generate a manifest describing all the necessary resources using `clusterctl`:

    $ clusterctl generate cluster \
        quickstart \
        --from templates/cluster-template.yaml \
        --kubernetes-version 1.34.3 \
        --control-plane-machine-count 3 \
        --worker-machine-count 3 \
        > cluster-quickstart.yaml

Apply the rendered quickstart manifest to the management cluster:

    $ kubectl apply -f cluster-quickstart.yaml

In order to configure the workload cluster, we'll need to fetch its kubeconfig:

    $ kubectl get secret quickstart-kubeconfig -o jsonpath='{.data.value}' | base64 -d > /tmp/quickstart
