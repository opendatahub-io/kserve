# AI / Model Serving / LLMs — Monitoring Dashboards

This directory contains Grafana-compatible dashboards for monitoring LLMInferenceService deployments. The dashboards are designed for the OpenShift Console monitoring plugin and are compatible with both the Administrator and Developer (ODC) perspectives.

## Dashboard Architecture

The dashboards are organized as a drill-down hierarchy:

```
Cluster Health Overview            "Is my cluster healthy?"
    |                       \
    v                        v
Model Performance & Usage    Failure & Diagnostics
    |                         "What kind of failure? Which component?"
    v
Replica Detail View           "Which specific pod is the problem?"
```

All dashboards include navigation links to move between levels without leaving the dashboard UI.

## Dashboard Overview

### 1. Cluster Health Overview (`model-serving-llms-cluster-health-odc.json`)
**Audience**: Platform Operators, SREs
**UID**: `model-serving-llms-cluster-health`
**Default Time Range**: 1 hour
**Perspective**: Administrator only

Single-pane-of-glass SLI-based health view. An operator opens this first and knows within seconds if the cluster is healthy.

#### Key Metrics:
- **SLI Summary Gauges**: Total request rate, HTTP error rate %, E2E latency P99, ready pods
- **Per-Model Health**: Request rate, error rate, and latency by model with drill-down links
- **Capacity & Scheduling**: KV cache utilization, request queue depth, and ready pods by pool
- **Data Staleness Detector**: Seconds since last metric scrape per model (warns at 60s, critical at 300s)
- **Token Throughput**: Cluster-wide input/output token processing rate

### 2. Model Performance & Usage (`model-serving-llms-model-performance-usage-odc.json`)
**Audience**: Data Scientists, Model Owners, MLOps Engineers
**UID**: `model-serving-llms-model-performance`
**Default Time Range**: 6 hours
**Perspective**: Administrator + Developer (ODC)

Detailed per-model performance with phase and topology awareness.

#### Key Metrics:
- **Error Rate**: HTTP-based error percentage with threshold alerts (1% warning, 5% critical)
- **Request Volume**: Total and per-component request throughput
- **Latency Analysis**: End-to-end, TTFT, and TPOT percentile tracking (P50, P90, P95)
- **Token Metrics**: Input vs output consumption rates, inter-token latency (P50/P95/P99), and per-request distribution
- **Phase Breakdown** (Prefill/Decode): KV cache, queue depth, TTFT, prefill time, decode time split by phase
- **Wide EP Topology**: Request volume and KV cache by component (leader/worker)
- **Caching Efficiency**: Prefix cache hit rate and preemption rate
- **Scheduling vs Capacity**: Queue time vs inference time comparison, scheduler queue depth

### 3. Replica Detail View (`model-serving-llms-replica-detail-odc.json`)
**Audience**: Infrastructure Engineers, DevOps
**UID**: `model-serving-llms-replica-detail`
**Default Time Range**: 6 hours
**Perspective**: Administrator + Developer (ODC)

Pod-level granularity for identifying hot spots and misbehaving replicas.

#### Key Metrics:
- **Per-Replica**: Request rate, error rate, E2E latency, TTFT, TPOT, queue time, KV cache, tokens
- **Phase Diagnostics per Replica**: Prefill time, decode time, prefix cache hit rate
- **Preemptions per Replica**: Identifies pods under KV cache memory pressure
- **Iteration Tokens per Replica**: Batch size consistency across pods
- **KV Offload & Inter-Token Latency**: KV offload throughput (bytes/s), KV offload time, inter-token latency P99 per replica

### 4. Failure & Diagnostics (`model-serving-llms-failure-diagnostics-odc.json`)
**Audience**: SREs, Platform Operators during incident response
**UID**: `model-serving-llms-failure-diagnostics`
**Default Time Range**: 6 hours
**Perspective**: Administrator + Developer (ODC)

Failure categorization by type and functional area attribution.

#### Key Metrics:
- **Service-Level Failures**: HTTP success vs error rates, vLLM request outcomes (abort/stop/length)
- **Functional Area Attribution**: Controller reconcile errors (routing/scheduling), workqueue health, memory pressure signals
- **Failures by Phase & Topology**: Abort rate by phase (prefill/decode) and by component (leader/worker)
- **Caching Diagnostics**: Prefix cache hit rate, prefix indexer size
- **Scheduling vs Capacity**: Queue time vs inference time by phase, scheduler pool utilization
- **EPP Scheduling & Flow Control**: Gateway errors by error code, scheduling attempt success rate, EPP scheduling latency P99, plugin processing latency P99, flow control queue duration P99

## Metrics and Data Sources

### Available Metrics
The dashboards use Prometheus metrics with the `kserve_` prefix, collected via:
- **PodMonitor**: vLLM engine metrics from port 8000
- **ServiceMonitor**: Scheduler metrics with RBAC authentication

### Key Labels
- `llm_isvc_name`: LLMInferenceService identifier (model proxy)
- `llm_isvc_role`: Phase role — `prefill` or `decode` (vLLM pods only, disaggregated deployments)
- `llm_isvc_component`: Component type — `workload`, `workload-prefill`, `workload-worker`, `workload-leader`, `router-scheduler`
- `namespace`: Kubernetes namespace
- `pod`: Pod name

### Core Metric Examples
```promql
# Request rate
rate(kserve_vllm:request_success_total[5m])

# Error rate (HTTP-based)
100 * (sum(rate(kserve_http_requests_total{status!="2xx"}[5m])) / (sum(rate(kserve_http_requests_total[5m])) > 0))

# P95 latency
histogram_quantile(0.95, rate(kserve_vllm:time_to_first_token_seconds_bucket[5m]))

# Token throughput
rate(kserve_vllm:prompt_tokens_total[5m]) + rate(kserve_vllm:generation_tokens_total[5m])

# KV cache by phase
avg(kserve_vllm:kv_cache_usage_perc{llm_isvc_role="prefill"}) * 100

# Scheduling delay vs capacity
histogram_quantile(0.95, sum(rate(kserve_vllm:request_queue_time_seconds_bucket[5m])) by (le))
histogram_quantile(0.95, sum(rate(kserve_vllm:request_inference_time_seconds_bucket[5m])) by (le))

# Prefix cache hit rate
100 * sum(rate(kserve_vllm:prefix_cache_hits_total[5m])) / sum(rate(kserve_vllm:prefix_cache_queries_total[5m]))

# Staleness detection
time() - max(timestamp(kserve_vllm:num_requests_running)) by (llm_isvc_name)
```

## Dashboard Variables

### Common Variables
- **datasource**: Prometheus data source selection
- **namespace**: Filter by Kubernetes namespace (multi-select with "All" option)
- **llm_isvc_name**: Filter by LLMInferenceService name (multi-select with "All" option)

### Phase/Topology Variables (Model Performance, Replica Detail, Failure & Diagnostics)
- **llm_isvc_role**: Filter by phase role — Prefill/Decode (multi-select with "All" option)
- **llm_isvc_component**: Filter by component type (Replica Detail only)

### Time Ranges
Available refresh intervals: 10s, 30s, 1m, 5m, 15m, 30m, 1h, 2h, 1d

## Drill-Down Navigation

### From Cluster Health Overview
- Click any per-model series → **Model Performance & Usage** (pre-filtered to that model)
- Click error rate series → **Failure & Diagnostics** (pre-filtered to that model)
- Dashboard header links → All other dashboards

### Interpreting Scheduling vs Capacity
- **Queue time >> Inference time**: Problem is insufficient replicas or scheduling delay. Scale up pods.
- **Inference time high, Queue time low**: Problem is within the engine (compute, KV cache). Check KV cache utilization and preemptions.
- **High preemptions + High KV cache**: Memory pressure. Scale up or reduce concurrent requests.

## OCP Console Compatibility

These dashboards are rendered by the [openshift/monitoring-plugin](https://github.com/openshift/monitoring-plugin), not native Grafana. The monitoring plugin loads Grafana-format JSON from ConfigMaps in `openshift-config-managed` with label `console.openshift.io/dashboard=true`, but only supports a subset of Grafana's features.

### Supported Panel Types

The monitoring plugin supports exactly these panel types (see `monitoring-plugin/web/src/components/dashboards/legacy/legacy-dashboard.tsx`):

| Panel Type | Rendered As | Used In Our Dashboards |
|------------|-------------|----------------------|
| `graph` | Time-series line/area chart | All dashboards |
| `gauge` | Single value display (same as `singlestat`) | Cluster Health (SLI gauges) |
| `singlestat` | Single value display | - |
| `table` | Data table | - |
| `grafana-piechart-panel` | Bar chart | - |
| `row` | Collapsible section header | All dashboards |

Panel types not in this list (e.g., `timeseries`, `stat`, `bargauge`) are **silently dropped** — they render as nothing.

### Layout System

The monitoring plugin uses a **12-column grid** via the `span` property on each panel, not Grafana's native 24-column `gridPos` system.

The width resolution order is:
1. `panel.span` — integer 1-12 (preferred, what we use)
2. `panel.breakpoint` — percentage string (e.g., `"50%"`)
3. Default: `12` (full width)

**`gridPos.w` is ignored for layout.** We keep `gridPos` for Grafana import compatibility but rely on `span` for OCP Console rendering. The mapping: `span = gridPos.w / 2`.

### Dashboard Format Requirements

- **Schema Version**: `schemaVersion: 22` (the plugin does not enforce a specific version but relies on field presence)
- **Datasource**: Simple string `"datasource": "$datasource"` — structured datasource objects are not supported
- **Panel layout**: Flat `panels` array with `type: "row"` panels as section markers. The plugin groups non-row panels under the preceding row panel automatically
- **Variables**: `templating.list` with `type: "query"` and `type: "interval"` are supported
- **NaN Protection**: All queries use `or vector(0)` fallback; histogram quantiles use `(... >= 0) or vector(0)` to handle NaN from empty buckets

### Access Control Labels

| Label | Effect |
|-------|--------|
| `console.openshift.io/dashboard: "true"` | Dashboard visible in Administrator perspective |
| `console.openshift.io/odc-dashboard: "true"` | Dashboard also visible in Developer (ODC) perspective (namespace-scoped) |

Cluster Health Overview is **Admin-only** because it queries cluster-wide metrics (controller reconcile errors, cross-namespace aggregation). The other 3 dashboards are visible in both perspectives since EPP and vLLM run in user namespaces.

## Deployment Topology Support

| Topology | Labels Available | Dashboard Coverage |
|----------|-----------------|-------------------|
| **Single-node** | `llm_isvc_component=workload` | All dashboards |
| **Multi-node (Wide EP)** | `llm_isvc_component=workload-leader/worker` | Wide EP rows in Model Performance, Component breakdown in Failure & Diagnostics |
| **Prefill/Decode Disaggregation** | `llm_isvc_role=prefill/decode` | Phase Breakdown rows in Model Performance, Phase panels in Failure & Diagnostics |

## Verifying Dashboard Deployment

Check if dashboards are deployed:
```bash
kubectl get configmap -n openshift-config-managed -l console.openshift.io/dashboard=true,app.kubernetes.io/part-of=kserve
```

Check monitoring pipeline is active:
```promql
count(up{job=~".*kserve-llm-isvc.*"}) > 0
```

## Installation

1. Import the JSON files into your Grafana instance or deploy via kustomize
2. Configure Prometheus data source with appropriate RBAC permissions
3. Ensure LLMInferenceService monitoring is enabled (see `pkg/controller/llmisvc/monitoring.go`)
4. Verify metric collection via PodMonitor and ServiceMonitor resources

## Known Metric Gaps

| Area | Gap | Workaround |
|------|-----|------------|
| Gateway-level errors | No ingress/Envoy routing errors | HTTP error rate covers vLLM-level errors |
| GPU utilization | Requires DCGM exporter | Out of scope — integrate DCGM separately |
| HTTP status sub-categories | vLLM may only emit `status="2xx"` | All non-2xx lumped as errors |
| KV transfer failures | No metric for P/D transfer issues | Monitor prefill/decode latency divergence |
| Autoscaler events | No scale-up/down decision metrics | Track ready pod count over time |
| NCCL/inter-node failures | Not instrumented in vLLM | Monitor Wide EP component abort rates |

## Troubleshooting

### Common Issues
1. **No data in panels**: Verify LLMInferenceService is running and metrics collection is enabled
2. **Permission errors**: Check ServiceAccount and ClusterRoleBinding for metrics access
3. **Missing labels**: Ensure relabeling configurations are applied in monitoring.go
4. **Phase filters empty**: `llm_isvc_role` only appears on disaggregated deployments
5. **Staleness panel shows high values**: Check PodMonitor/ServiceMonitor scrape targets with `up{job=~".*kserve.*"}`

### Debug Queries
```promql
# Check if metrics are being collected
{__name__=~"kserve_.*"}

# Verify label presence
kserve_vllm:request_success_total{llm_isvc_name!=""}

# Check monitoring components
up{job=~".*kserve.*"}

# Verify phase labels exist
kserve_vllm:request_success_total{llm_isvc_role!=""}
```
