# Reflex 🚀✨

> [!WARNING]
> **Experimental / Work In Progress**
> This repository is currently a sandbox for experimenting and playing around with the [Jev](https://jev.com) and [Laya](https://github.com/NandhaKishorM/laya) decision model APIs. It is not currently intended for production use.

**Intelligent Observability and Remediation Proxy**

Reflex is a high-throughput Go proxy that ingests infrastructure alerts via HTTP webhooks, integrates with decision model APIs (like Jev and local Laya instances) to evaluate them concurrently, and routes them to either an auto-remediation runbook (if it's a known slay) or a human escalation queue (if it needs adult supervision).

1. **Wasm-Shield**: Secure WebAssembly runbook execution via `wazero`.
2. **MoA Evaluator**: Mixture-of-Agents consensus to prevent LLM hallucinations.
3. **Storm-Breaker**: Sliding-window deduplicator squashing "Thundering Herd" alert storms.
4. **Stateful DAGs**: Runbooks execute as LIFO state-machines with automatic compensation rollbacks.
5. **Kubernetes Checkpointing**: Distributed checkpointing and Orphan Watcher for stateless crash-recovery via K8s ConfigMaps.
6. **Wasm Registry**: Fetches compiled runbooks over HTTP at runtime.

## 🏗️ Architecture

We use standard Go concurrency (goroutines and buffered channels) to keep things completely non-blocking and prevent deadlocks during high-traffic bursts.

```mermaid
graph TD
    A[External Prometheus/Datadog] -->|POST /webhook| B(HTTP Ingress Server)
    B -->|alertChan| S(Storm-Breaker Deduplicator)
    S -->|dedupeChan| C(MoA Evaluator Workers)
    C -->|Evaluate API Call| Z[Decision API: Jev or Laya]
    Z -->|Consensus Result| C
    C -->|eventChan| D(Router)
    D -->|High Confidence, Low Sev| E[DAG Runbook Engine]
    E <-->|Checkpoint State| V[(K8s API Server)]
    V -->|State Recovered| E
    E -->|Execute| W(Wasm-Shield Runtime)
    D -->|Low Confidence OR High Sev| G[Escalate to Slack/PagerDuty]
```

## 🧠 Routing Logic

### Condition A (Auto-Remediate) ✅
- `domain` is known
- `severity` < 8.0
- `auto_recoverable` > 0.60
- overall `confidence` >= 0.60

### Condition B (Human Escalation) 🚨
- `confidence` < 0.60 OR `severity` >= 8.0

## 📖 Configuration (`runbooks.yaml`)

Reflex maps failing components to stateful execution DAGs via `runbooks.yaml`. 
You can provide **local scripts**, or dynamically fetch Wasm runbooks directly from a **Remote Registry (HTTP)**.

```yaml
runbooks:
  database:
    steps:
      - name: "Drain Connections"
        action: "http://localhost:9090/drain-connections.wasm"
        compensate: "http://localhost:9090/restore-connections.wasm"
      - name: "Restart DB"
        action: "./scripts/restart-redis.wasm"
        compensate: "./scripts/rollback-redis.wasm"
```

*Note: If an action fails, Reflex automatically pauses the DAG and executes the `compensate` steps in reverse order (LIFO) to safely rollback the system to its original state.*

## 🛠️ Demo Time!

Make sure you have [Just](https://github.com/casey/just) installed.

### Option A: Use the fully local Laya Microservice (Free Alternative)
We built a thin Python wrapper around [Laya](https://github.com/NandhaKishorM/laya) that mimics the Jev API identically, so you can test the system locally for free!

1. **Start the Laya Server** (Requires Python 3.10+)
   ```bash
   just run-laya-server
   ```
   *Note: This will download the Laya checkpoints (~400MB) on the first run.*
2. **Start the Proxy Server**
   ```bash
   just run-laya
   ```

### Option B: Use the Mock or Real Jev API
```bash
# Uses the Mock Jev Client (No API Key Required)
# Note: Ensure you have a valid kubeconfig (~/.kube/config) to access the K8s API
just run

# OR: Use the Real Jev API
JEV_API_KEY=your_actual_key just run-real
```

### 3. Blast it with Traffic

Open a **separate terminal window** and fire the load generator. This will instantly shoot 50 concurrent JSON webhook payloads at your proxy.

```bash
just load
```

Watch the terminal light up with beautiful terminal colors as the router instantly processes and routes the alerts! ✨

## 🧪 End-to-End Local Testing
Want to test the full auto-remediation pipeline with a real local Kubernetes cluster?
Check out the **[End-to-End Kubernetes Testing Guide](TESTING.md)** for detailed instructions on spinning up the cluster and verifying runbooks locally on Windows (WSL), Linux, or macOS.
