#!/bin/bash
set -e

echo "⚠️  NOTE: This script requires a working WSL2/Hyper-V environment to run k3d."
echo "If you see a 'Virtual Machine Platform' error, please enable it in your BIOS and OS."
echo ""

# 1. Create a local k3d cluster
echo "Cluster creation..."
# k3d cluster create reflex-test --servers 1

# 2. Deploy Redis
echo "Deploying Redis..."
# helm repo add bitnami https://charts.bitnami.com/bitnami
# helm install test-redis bitnami/redis --set architecture=standalone

# 3. Deploy Prometheus
echo "Deploying Prometheus..."
# helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
# helm install prometheus prometheus-community/prometheus

echo "Once your environment is fixed, uncomment the commands above to provision the integration test suite."
