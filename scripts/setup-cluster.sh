#!/bin/bash
set -e

CLUSTER_NAME="reflex-cluster"

export DOCKER_HOST=unix:///run/user/1000/podman/podman.sock
export DOCKER_SOCK=/run/user/1000/podman/podman.sock

echo "=== Creating k3d cluster ==="
k3d cluster delete $CLUSTER_NAME || true
k3d cluster create $CLUSTER_NAME --no-lb --api-port 127.0.0.1:6550 \
  --k3s-arg '--kubelet-arg=feature-gates=KubeletInUserNamespace=true@server:*' \
  --wait

echo "=== Adding Prometheus Helm Repo ==="
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update

echo "=== Installing Prometheus Stack ==="
helm install prometheus prometheus-community/kube-prometheus-stack \
  --namespace default \
  --wait

echo "=== Applying Kubernetes Manifests ==="
# Navigate to project root to apply manifests
cd "$(dirname "$0")/.."
kubectl apply -f k8s/redis.yaml
kubectl apply -f k8s/sample-app.yaml
kubectl apply -f k8s/prometheus-rules.yaml
kubectl apply -f k8s/alertmanager-config.yaml

echo "=== Setup Complete! ==="
echo "You can now run reflex locally and port-forward Alertmanager to trigger alerts:"
echo "kubectl port-forward svc/prometheus-kube-prometheus-alertmanager 9093:9093"
