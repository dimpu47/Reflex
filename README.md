# Reflex 🚀✨

> [!WARNING]
> **Experimental / Work In Progress**
> This repository is a sandbox for an Intelligent Observability and Remediation Proxy. It is not currently intended for production use.

**Intelligent Observability and Remediation Proxy**

Reflex is a high-throughput Go proxy that ingests infrastructure alerts via HTTP webhooks, evaluates them concurrently using local AI (Ollama), and routes them to either an auto-remediation runbook or a human escalation queue.

## 🎬 Live Interactive Demo

Reflex pairs autonomous AI reasoning with an enterprise **Human-in-the-Loop (HITL)** dashboard and strict **Kubernetes Cluster Checkpointing**.

### 1. Web UI Operator Flow (Diffs, Pod Logs, & 1-Click Rollback)
Review proposed cluster mutations before execution. Inspect unified Git-style diffs (`+`/`-`), stream live failing pod stdout/stderr, verify K8s events, and execute 1-click compensation rollbacks.

[![Reflex Web UI Demo](assets/reflex_ui_demo.gif)](assets/reflex_ui_demo.mp4)
> 💡 *Click image to open the [pausable MP4 video](assets/reflex_ui_demo.mp4).*

<details>
<summary>📸 <b>Click to expand step-by-step UI breakdown (Pause & Inspect)</b></summary>

#### Tab 1: Cluster State Diff (Proposed Mutation)
Unified git-style diff displaying current failing state in red (`-`) and proposed corrective actions in green (`+`).
![Cluster State Diff](assets/ui/diff_tab.png)

#### Tab 2: Live Pod Terminal Logs
Dark terminal streaming the last 50 lines of logs directly from the failing container with line numbers.
![Pod Recent Logs](assets/ui/logs_tab.png)

#### Tab 3: Execution History & Applied Cluster State Diff
Audit log of completed DAGs showing per-step execution stdout, applied diffs, and the 1-click compensation rollback button.
![Runbook Execution History](assets/ui/history_tab.png)

</details>

---

### 2. Kubernetes Cluster Remediation Pipeline
From Prometheus Alertmanager webhook ingress to Wazero WebAssembly sandbox execution and distributed ConfigMap checkpointing.

[![Reflex Cluster Lifecycle Pipeline](assets/reflex_cluster_flow.gif)](assets/reflex_cluster_flow.mp4)
> 💡 *Click image to open the [pausable MP4 video](assets/reflex_cluster_flow.mp4).*

<details>
<summary>🔍 <b>Click to expand individual pipeline stages (Pause & Read each step)</b></summary>

| Stage | Pipeline Milestone | Description |
| :--- | :--- | :--- |
| **Stage 1** | **Alert Ingress & K8s Enrichment** | HTTP POST `/webhook` ingests alert; in-cluster K8s API client queries last 50 pod logs + 5 recent namespace events. |
| **Stage 2** | **Storm-Breaker Dedupe & RAG** | 5-second sliding window squashes thundering herd alert storms; queries vector store for historical reliability. |
| **Stage 3** | **MoA Consensus & OPA Gate** | Mixture-of-Agents classifies failure domain and auto-recoverability; OPA verifies `policy.rego` host permissions. |
| **Stage 4** | **Human-in-the-Loop Review** | Production gate pauses execution; operator reviews unified diff and pod logs in Web UI before approving. |
| **Stage 5** | **Wasm Sandbox Execution** | `wazero` executes isolated WebAssembly module; host functions dispatch authorized `kubectl` commands to K8s API. |
| **Stage 6** | **ConfigMap Checkpointing & Rollback** | Completed steps are checkpointed in K8s ConfigMaps with leader leases; inverse compensation actions cached for 1-click rollback. |

#### Stage Slides:
1. **Ingress & Enrichment:** ![Stage 1](assets/stages/stage1_ingress.png)
2. **Storm-Breaker & RAG:** ![Stage 2](assets/stages/stage2_dedupe_rag.png)
3. **MoA & OPA Policy:** ![Stage 3](assets/stages/stage3_moa_opa.png)
4. **HITL Review:** ![Stage 4](assets/stages/stage4_hitl.png)
5. **Wasm Execution:** ![Stage 5](assets/stages/stage5_wasm_exec.png)
6. **State Persistence & Rollback:** ![Stage 6](assets/stages/stage6_checkpoint_rollback.png)

</details>

---

## 🌟 Core Features

1. **Wasm-Shield**: Secure WebAssembly runbook execution via `wazero` with strict Open Policy Agent (OPA) host-call authorization.
2. **MoA Evaluator**: Mixture-of-Agents consensus to prevent LLM hallucinations.
3. **Storm-Breaker**: Sliding-window deduplicator squashing "Thundering Herd" alert storms across microservice fleets using RAG embeddings.
4. **JIT Coder Agent**: On-the-fly autonomous Go WebAssembly generation. When Reflex encounters a failing domain it doesn't have a static runbook for, it dynamically writes, compiles, and executes a Wasm runbook using local LLMs.
5. **Environment-Aware HITL (Human-in-the-Loop)**: Production environments require explicit human approval via the embedded `/ui/` dashboard displaying unified cluster state diffs and pod logs. Staging environments auto-execute for rapid chaos testing.
6. **Stateful DAGs & LIFO Compensation**: Runbooks execute as state-machines with automatic inverse compensation rollbacks (`./scripts/rollback-*.wasm`).
7. **Kubernetes Checkpointing**: Distributed checkpointing and Orphan Watcher for stateless crash-recovery via K8s ConfigMaps.

## 🏗️ Architecture

We use standard Go concurrency (goroutines and buffered channels) to keep things completely non-blocking and prevent deadlocks during high-traffic bursts.

```mermaid
graph TD
    A[External Prometheus/Alertmanager] -->|POST /webhook| B(HTTP Ingress Server)
    B -->|alertChan| S(Storm-Breaker Deduplicator)
    S -->|dedupeChan| C(MoA Evaluator Workers)
    C -->|Evaluate| Z[Local Ollama]
    Z -->|Consensus Result| C
    C -->|eventChan| D(Router)
    D -->|High Confidence, Low Sev| E[DAG Runbook Engine]
    E -->|Check domain| M{Static Runbook?}
    M -->|No| J[JIT Coder Agent]
    J -->|Generate Go & Compile to Wasm| W(Wasm-Shield Runtime)
    M -->|Yes| W
    W -->|Pending Approval Queue| U[HITL Web UI]
    U -->|Approved| K[K8s API via Host Function]
    D -->|Low Confidence OR High Sev| G[Escalate to Slack/PagerDuty]
```

## 📖 Configuration (`runbooks.yaml` & `reflex.yaml`)

Reflex maps failing components to stateful execution DAGs via `runbooks.yaml`. 
You can provide **local scripts**, or dynamically fetch Wasm runbooks directly from a **Remote Registry (HTTP)**.

```yaml
runbooks:
  database:
    steps:
      - name: "Restart DB"
        action: "./scripts/restart-redis.wasm"
        compensate: "./scripts/rollback-redis.wasm"
```

If a domain (e.g., `network`) is NOT mapped, the **JIT Coder Agent** will generate one on the fly.

**Environment variables (`reflex.yaml`) control HITL:**
- `REFLEX_ENV=staging`: Auto-execute all runbooks instantly.
- `REFLEX_ENV=production`: Runbooks pause in the pending queue for manual human approval via the Web UI (`http://localhost:8080/ui/`).

### 🛡️ SRE Architecture Note: Pod Scaling vs. Connection Draining

A critical distinction in production reliability engineering:
- **Scaling Pods to 0 (`kubectl scale --replicas=0`)**: Changes desired replica count. Kubernetes terminates the pod immediately. Active TCP sockets receive `ECONNRESET`, in-flight requests abort, and datastores (like Redis) experience a hard outage. In Reflex's local sandbox, this was initially used as a fast demonstration of host-level mutations.
- **True Connection Draining**: Allows in-flight queries/requests to finish while cleanly redirecting new traffic:
  - **Stateless Services (`sample-app`, `order-service`)**: Rolling restarts (`kubectl rollout restart deploy/<name>`) or EndpointSlice traffic shedding ensure zero downtime while draining active HTTP keep-alive connections.
  - **Stateful Datastores (Redis)**: Application-level draining requires protocol commands (`CLIENT PAUSE <ms>` or `CLIENT KILL TYPE normal`) or Sentinel/Cluster failover before graceful pod restarts with `preStop` flush hooks.

Reflex runbooks enforce rolling updates and health probes across the microservice fleet to ensure zero dropped client traffic.

## 📟 Integrations
- **Alertmanager Ingestion:** The `/webhook` endpoint natively supports the Prometheus/Alertmanager array payload format, meaning no translation proxy is required between your Alertmanager receivers and Reflex.
- **Slack/PagerDuty Escalations:** When the MoA Engine generates a low-confidence decision or evaluates a highly severe unrecoverable alert, it instantly fires a Slack-compatible JSON webhook payload alerting human operators for manual intervention.

## 🧪 End-to-End Local Testing
Want to test the full auto-remediation pipeline with a real local Kubernetes cluster?
Check out the **[End-to-End Kubernetes Testing Guide](TESTING.md)** for detailed instructions on spinning up the cluster and verifying runbooks locally.
