#!/bin/bash
echo "Restarting Sample App Deployment in Kubernetes..."
kubectl rollout restart deployment sample-app -n default
