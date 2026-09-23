#!/bin/bash
echo "Restarting Redis Deployment in Kubernetes..."
kubectl rollout restart deployment redis -n default
