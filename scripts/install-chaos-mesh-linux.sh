#!/bin/bash
set -e

echo "=== Adding Chaos Mesh Helm Repo ==="
helm repo add chaos-mesh https://charts.chaos-mesh.org
helm repo update

echo "=== Installing Chaos Mesh ==="
kubectl create ns chaos-mesh || true

# We specify containerd as the runtime since k3d uses k3s which uses containerd internally.
helm install chaos-mesh chaos-mesh/chaos-mesh \
  --namespace chaos-mesh \
  --set chaosDaemon.runtime=containerd \
  --set chaosDaemon.socketPath=/run/k3s/containerd/containerd.sock \
  --version 2.6.2 \
  --wait

echo "=== Chaos Mesh Installed! ==="
echo "You can forward the Chaos Dashboard if you want a UI:"
echo "kubectl port-forward svc/chaos-dashboard 2333:2333 -n chaos-mesh"
