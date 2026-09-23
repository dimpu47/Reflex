# End-to-End Kubernetes Testing

This guide explains how to test the `reflex` auto-remediation flow locally using a local Kubernetes cluster (k3d). When an alert is sent to `reflex`, it will evaluate the alert, determine the failing component, and execute a local bash runbook (e.g., `./scripts/restart-redis.sh`) to automatically remediate the issue via `kubectl`.

---

## 🪟 Windows (WSL + Rootless Podman)
*These steps are specifically tailored for setups using WSL2 and rootless Podman.*

### 1. Prevent WSL Suspension
If you don't have an active WSL terminal open, Windows may suspend the WSL VM after a short idle period, which will instantly kill your rootless Podman containers. To prevent this during testing, open a background PowerShell tab and run:
```powershell
wsl bash -c "sleep infinity"
```

### 2. Configure Rootless Podman & Set Permissions
Ensure your `DOCKER_HOST` is pointing to the rootless Podman socket, and make the runbook scripts executable. From inside WSL (or via `wsl bash -c`):
```bash
export DOCKER_HOST=unix:///run/user/1000/podman/podman.sock
chmod +x ./scripts/*.sh
```

### 3. Spin up the Local Cluster
Execute the cluster setup script. This script automatically spins up a `k3d` cluster (with special flags `KubeletInUserNamespace=true` required for rootless environments), installs Prometheus, and deploys the sample `redis` application.
```powershell
wsl bash ./scripts/setup-cluster.sh
```

### 4. Start Reflex
In a regular Windows PowerShell terminal, start the Go server:
```powershell
go run main.go
```

### 5. Trigger a Synthetic Alert
In another Windows terminal, simulate an Alertmanager webhook payload to trigger the auto-remediation pipeline. We use `curl.exe` to avoid PowerShell's `Invoke-WebRequest` parsing quirks:
```cmd
curl.exe -X POST http://localhost:8080/webhook -H "Content-Type: application/json" -d "{\"id\":\"test-1\",\"service_name\":\"redis\",\"payload\":{\"status\":\"firing\"}}"
```
You should see `reflex` log that it executed the runbook.

### 6. Verify Auto-Remediation
Check your cluster to confirm that the runbook successfully executed `kubectl rollout restart deployment redis`. The `redis` pod should show a very recent age (e.g., `10s`).
```powershell
wsl kubectl get pods
```

---

## 🐧🍎 Generic Linux & macOS (Docker)
*These steps apply for standard environments running Docker Desktop or native Docker daemon.*

### 1. Prerequisites
Ensure you have the following installed:
- [Docker](https://docs.docker.com/get-docker/)
- [k3d](https://k3d.io/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/docs/intro/install/)

### 2. Set Script Permissions
Make sure the setup script and runbooks are executable:
```bash
chmod +x scripts/*.sh
```

### 3. Spin up the Local Cluster
Run the setup script. It will provision the k3d cluster, install Prometheus via Helm, and deploy the mock applications.
*(Note: If you are NOT using rootless podman, the `KubeletInUserNamespace` flag in the script is safely ignored by k3s).*
```bash
./scripts/setup-cluster.sh
```

### 4. Start Reflex
Start the proxy server in your terminal:
```bash
go run main.go
```

### 5. Trigger a Synthetic Alert
In a new terminal window, send a JSON payload mimicking a webhook from Prometheus Alertmanager:
```bash
curl -X POST http://localhost:8080/webhook \
  -H "Content-Type: application/json" \
  -d '{"id":"test-1","service_name":"redis","payload":{"status":"firing"}}'
```

### 6. Verify Auto-Remediation
The `reflex` logs will output `[✅ AUTO-REMEDIATE]` and execute the `restart-redis.sh` bash script. Verify the pod rollout occurred:
```bash
kubectl get pods
```
You should see that the `redis` pod has a very recent `AGE`, confirming the runbook successfully remediated the simulated outage.
