# LLM InferenceService Dashboards — Use Cases & User Queries

This document maps real operational questions to the dashboard that answers them. Use it as a guide to understand what the monitoring suite covers and where to look when something goes wrong.

## Personas

| Persona                              | Role                             | Primary Concern                                       |
|--------------------------------------|----------------------------------|-------------------------------------------------------|
| **Platform Operator / SRE**          | Owns the serving infrastructure  | Is the cluster healthy? What broke?                   |
| **Data Scientist / Model Owner**     | Owns a specific model deployment | Is my model performing well? Where is the bottleneck? |
| **Infrastructure Engineer / DevOps** | Owns pod-level operations        | Which replica is misbehaving? Why?                    |

## Use Cases

### 1. Cluster-Wide Health Check

> "I just started my shift. Is everything running fine?"

**Dashboard**: Cluster Health Overview

Open the SLI summary gauges. Four numbers tell you the state of the world:

- **Total request rate** — is traffic flowing?
- **HTTP error rate %** — are requests failing? (green < 1%, yellow >= 1%, red >= 5%)
- **E2E latency P99** — worst-case user-facing latency
- **Ready pods** — do we have enough capacity?

If all four are green, the cluster is healthy. Move on.

### 2. Identifying a Problematic Model

> "Something is off. Which model is the problem?"

**Dashboard**: Cluster Health Overview → per-model rows

The per-model health panels show request rate, error rate, and latency by `llm_isvc_name`. The problematic model will stand out as a spike in error rate or latency. Click the model name to drill down into Model Performance or Failure & Diagnostics.

### 3. Understanding Model-Level Latency

> "Users are complaining about slow responses from model X. Where is the time going?"

**Dashboard**: Model Performance & Usage

Three latency panels sit side by side:

- **E2E Request Latency** (P50/P90/P95/P99/Average) — total time from request to response
- **Time to First Token (TTFT)** — how long until the user sees the first token
- **Time per Output Token (TPOT)** — how fast tokens stream after the first one

If TTFT is high → the problem is in prefill (prompt processing). If TPOT is high → the problem is in decode (token generation). If both are normal but E2E is high → look at queue time.

The **Inter-Token Latency** panel (P50/P95/P99) shows token delivery smoothness — high values mean choppy streaming even if average TPOT looks fine.

### 4. Scheduling Bottleneck vs Compute Bottleneck

> "Latency is high — is it because we need more replicas, or because the engine itself is slow?"

**Dashboard**: Model Performance & Usage → Scheduling Delay vs Capacity row

The **Queue Time vs Inference Time P95** panel directly answers this:

- **Queue time >> Inference time** → not enough replicas or scheduling delay. Scale up pods.
- **Inference time high, Queue time low** → engine bottleneck (compute or KV cache). Check KV cache utilization and preemptions.
- **Both high** → the system is saturated end to end.

### 5. Prefill vs Decode Phase Diagnosis

> "We run disaggregated Prefill/Decode. Which phase is the bottleneck?"

**Dashboard**: Model Performance & Usage → Prefill / Decode Phase Breakdown row

Use the `llm_isvc_role` variable to filter by phase. The row shows:

- **KV Cache by Phase** — is prefill or decode exhausting cache?
- **Requests Waiting by Phase** — which phase has the backlog?
- **TTFT by Phase** — time-to-first-token split by prefill vs decode
- **Prefill Time P95** and **Decode Time P95** — direct phase timing comparison

### 6. Wide EP / Multi-Node Topology Issues

> "We use multi-node inference with leader/worker topology. Are workers keeping up?"

**Dashboard**: Model Performance & Usage → Wide EP / Multi-Node Topology row

- **Request Volume by Component** — traffic distribution across leader/worker
- **KV Cache by Component** — which node type is running out of cache

An imbalance in request volume or a leader with saturated KV cache while workers are idle signals a routing or sharding problem.

### 7. Identifying a Misbehaving Replica

> "Overall metrics look fine, but some users report intermittent slowness."

**Dashboard**: Replica Detail View

Every metric is broken down by pod. Look for outliers:

- One pod with higher **E2E latency** or **TTFT** than peers → possible hardware issue or uneven routing
- One pod with high **KV cache utilization** while others are low → unbalanced request distribution
- One pod with high **preemptions** → that pod is under memory pressure
- One pod with low **prefix cache hit rate** → cache cold-start or routing not prefix-aware

### 8. Understanding Error Types During an Incident

> "Error rate spiked. What kind of errors? What component is failing?"

**Dashboard**: Failure & Diagnostics

Start with the top row:

- **HTTP Request Rate (Success vs Error)** — magnitude of the failure
- **vLLM Request Outcomes** — are requests being aborted (engine failure), hitting max length (expected), or completing normally?
- **Gateway Errors by Error Code** — specific error types at the EPP layer

Then drill into functional area attribution:

- **Controller Reconcile Errors** → scheduling/routing layer is failing
- **Workqueue Health** → scheduler is falling behind (high depth) or retrying (high retry rate)
- **Memory Pressure** → preemptions + KV cache saturation

### 9. Failure Attribution by Phase and Topology

> "Errors are coming from vLLM aborts. Is it the prefill or decode phase? Leaders or workers?"

**Dashboard**: Failure & Diagnostics → Failures by Phase & Topology row

- **Abort Rate by Phase** — pinpoints whether aborts concentrate in prefill or decode
- **Abort Rate by Component** — pinpoints whether aborts concentrate on leaders or workers in Wide EP

### 10. Prefix Caching Effectiveness

> "We enabled prefix caching. Is it actually working?"

**Dashboard**: Model Performance & Usage → Caching Efficiency row

- **Prefix Cache Hit Rate** — percentage of queries that hit the prefix cache. Should be >0 and ideally >50% for workloads with shared system prompts.
- **Preemptions Rate** — high preemptions alongside low cache hit rate suggests the cache is being evicted under memory pressure.

For scheduler-side verification: Failure & Diagnostics → **Prefix Indexer Size** panel — if this isn't growing with traffic, prefix-aware routing may not be functioning.

### 11. KV Offload Health in Disaggregated Deployments

> "We run Prefill/Decode disaggregation. Is the KV transfer between phases working?"

**Dashboard**: Replica Detail View → KV Offload & Inter-Token Latency row

- **KV Offload Throughput per Replica** — bytes/s of KV cache data being transferred. Zero means no transfers happening.
- **KV Offload Time per Replica** — time spent on transfers. High values indicate network or memory bandwidth constraints.
- **Inter-Token Latency P99 per Replica** — stalls in decode-phase token delivery may correlate with slow KV transfers.

### 12. EPP Scheduling Layer Health

> "The scheduler/EPP seems slow. Where is the overhead?"

**Dashboard**: Failure & Diagnostics → EPP Scheduling & Flow Control row

- **Scheduling Attempt Success Rate** — if failure rate is high, the scheduler can't find suitable endpoints
- **EPP Scheduling Latency P99** — total time from request arrival at EPP to endpoint selection
- **Plugin Processing Latency P99** — which scheduler plugin is slowest (filter, score, prefix-aware, etc.)
- **Flow Control Queue Duration P99** — if flow control is enabled, how long requests wait for admission

### 13. Capacity Planning and Token Economics

> "How much token throughput is this cluster handling? Are we approaching limits?"

**Dashboard**: Cluster Health Overview → Token Throughput panel (cluster-wide input + output tokens/s)

**Dashboard**: Model Performance & Usage → Token Consumption panel (per-model breakdown of input vs output tokens)

Compare token throughput trends against KV cache utilization and queue depth to assess headroom.

### 14. Data Freshness / Monitoring Pipeline Health

> "The dashboard shows no data. Is the monitoring pipeline broken?"

**Dashboard**: Cluster Health Overview → Data Staleness Detector

Shows seconds since last metric scrape per model. Warning at 60s, critical at 300s. If staleness is high:

1. Check PodMonitor / ServiceMonitor scrape targets: `up{job=~".*kserve.*"}`
2. Verify LLMInferenceService is running and metrics collection is enabled
3. Check RBAC permissions for the ServiceAccount

### 15. SLO Violation Detection

> "Are we meeting our latency and availability SLOs?"

**Dashboard**: Cluster Health Overview → SLO & Gateway Signals row

- **SLO Violations** — tracks `inference_objective_request_duration_seconds` against configured objectives
- **Gateway Error Rate** — errors at the gateway layer (distinct from vLLM-level errors)
- **Pool Saturation** — how close the pool is to capacity limits

## Drill-Down Workflow Summary

Most investigations follow this path:

```
1. Cluster Health Overview          "Is something wrong?"
         |
         | (identify the model)
         v
2a. Model Performance & Usage      "What kind of problem — latency, errors, capacity?"
         |
         | (need pod-level detail)
         v
3.  Replica Detail View             "Which specific pod? What's different about it?"

         — or —

2b. Failure & Diagnostics           "What type of failure? Which functional area?"
```

The dashboards pass namespace, model name, and time range between each other via navigation links. You never need to re-enter filters when drilling down.
