#!/bin/bash
echo "Starting Mock Prometheus Monitor..."
echo "Monitoring Redis pod status every 5 seconds..."

while true; do
  # Get the ready status of all Redis pods. If there are no ready pods, it returns empty or false
  READY=$(kubectl get pods -l app=redis -o jsonpath='{.items[*].status.containerStatuses[*].ready}')
  
  if [[ "$READY" != *"true"* ]]; then
    DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
    echo "$(date) - 🚨 REDIS IS DOWN! Firing Webhook to Reflex..."
    curl -s -X POST -H 'Content-Type: application/json' -d "@$DIR/../dummy_alert.json" http://localhost:8080/webhook > /dev/null
    echo "$(date) - Webhook sent. Waiting 30s for Reflex to remediate before checking again..."
    sleep 30
  fi
  sleep 5
done
