This directory contains Kubernetes manifests to start an instance of the rollout-operator locally.

To use it:

* Build the rollout-operator image: `make build-image`
* Make the image available to your Kubernetes cluster (not required for use with Docker Desktop)
* Apply the Kubernetes manifests: `./apply.sh`
* Port forward to the operator service: `kubectl --namespace=rollout-operator-development port-forward svc/rollout-operator 8080:80`

You'll then be able to access the rollout operator at `http://localhost:8080`.