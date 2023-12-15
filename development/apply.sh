#! /usr/bin/env bash

set -euo pipefail

SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )

current_context=$(kubectl config current-context)
echo "Applying changes to '$current_context' kubectl context. Is this OK?"

select yn in "Yes" "No"; do
    case $yn in
        Yes )
          break
          ;;
        No )
          exit
          ;;
    esac
done

kubectl apply --wait -f "$SCRIPT_DIR/namespace.yaml"
find "$SCRIPT_DIR" -type f -name '*.yaml' -not -name 'namespace.yaml' -exec kubectl apply --namespace=rollout-operator-development --wait -f {} \;
