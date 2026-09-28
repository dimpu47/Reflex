# End-to-End Kubernetes Testing

This guide explains how to test the `reflex` auto-remediation flow locally using a local Kubernetes cluster (k3d). 

We deploy Reflex *into* the cluster so it can natively use local Ollama models (via the host gateway) and perform WebAssembly runbook executions (like `kubectl rollout restart`) against the cluster.

---

## 🪟 Windows (WSL + Rootless Podman)

### 1. Configure Rootless Podman & Set Permissions
Ensure your `DOCKER_HOST` is pointing to the rootless Podman socket:
```bash
export DOCKER_HOST=unix:///run/user/1000/podman/podman.sock
export DOCKER_SOCK=/run/user/1000/podman/podman.sock
```

### 2. Spin up the Local Cluster
Execute the cluster setup script. This script automatically spins up a `k3d` cluster (with special flags `KubeletInUserNamespace=true` required for rootless environments) and deploys a sample application.
```bash
k3d cluster start reflex-cluster
```

### 3. Deploy Reflex
Build the Reflex container, load it into k3d, and apply the deployment:
```bash
# Build
podman build -t localhost/reflex:latest .

# Import into k3d
podman save localhost/reflex:latest | podman exec -i k3d-reflex-cluster-server-0 ctr images import -

# Deploy
kubectl apply -f k8s/reflex.yaml
kubectl rollout restart deploy/reflex
kubectl rollout status deploy/reflex --timeout=60s
```

### 4. Setup Ollama Proxy (Local Testing)
If Ollama is running on the WSL host at `localhost:11434`, Reflex inside the cluster needs to reach it. Set up a socat proxy so the pod can reach it via `172.25.81.153:11435` (or your equivalent host IP):
```bash
socat TCP-LISTEN:11435,fork,bind=0.0.0.0 TCP:127.0.0.1:11434 &
```

### 5. Trigger a Synthetic Alert
Port-forward the Reflex webhook service:
```bash
kubectl port-forward svc/reflex 8080:8080 &
```

In another terminal, send a JSON payload mimicking a webhook from Alertmanager:
```bash
curl -X POST http://localhost:8080/webhook -H "Content-Type: application/json" -d @payload.json
```

### 6. Verify Auto-Remediation & JIT Generation
Check the Reflex logs to see the MoA consensus, the JIT Coder Agent generating the Go Wasm code, the compilation, and the execution:
```bash
kubectl logs -l app=reflex --tail=200 -f
```
You should see:
1. `[RAG] Fetching embeddings...`
2. `MoA Consensus Reached!`
3. `[Coder Agent] Generating Go Wasm Runbook...` (with extraction logs)
4. `[JIT Compiler] Compiling...`
5. `[STAGING] Non-production environment. Auto-executing...`
6. `[EXEC-OK] kubectl rollout restart deployment/sample-app`
