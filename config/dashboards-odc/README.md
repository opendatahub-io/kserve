# LLM InferenceService Monitoring Dashboards

This directory contains Grafana dashboards for monitoring LLMInferenceService deployments. The dashboards are designed to be compatible with OpenShift Developer Console (ODC) and OpenDataHub (ODH) environments.

## Dashboard Overview

### LLM InferenceService Model Performance & Usage Dashboard (`llm-isvc-model-performance-usage-odc.json`)
**Audience**: Data Scientists, Model Owners, Business Leaders, MLOps Engineers
**UID**: `llm-isvc-model-performance`
**Default Time Range**: 24 hours

This comprehensive dashboard provides complete visibility into LLMInferenceService performance, combining executive-level KPIs with detailed technical metrics for thorough analysis.

#### Key Metrics:
- **KV Cache Utilization**: Per-component cache usage with warning/critical thresholds (70%/90%)
- **Error Rate**: Real-time error percentage tracking over time with threshold alerts
- **Request Volume**: Total and per-component request throughput analysis
- **Comprehensive Latency Analysis**: End-to-end, TTFT, and TPOT percentile tracking (P50, P90, P95)
- **Token Metrics**: Consumption rates and average tokens per request distribution

#### Features:
- **Multi-dimensional Filtering**: Namespace and LLMInferenceService filtering with "All" options
- **Complete Latency Lifecycle**: End-to-end → Time to First Token → Time per Output Token coverage
- **Component-level Insights**: Per-component KV cache utilization and request volume breakdown
- **Token Analysis**: Input vs output token consumption and per-request distribution
- **Error Monitoring**: Time series error rate tracking with visual threshold indicators
- **Production-ready**: NaN-protected queries with fallback values for reliable dashboard operation

## Metrics and Data Sources

### Available Metrics
The dashboards use Prometheus metrics with the `kserve_` prefix, collected via:
- **PodMonitor**: vLLM engine metrics from port 8000
- **ServiceMonitor**: Scheduler metrics with RBAC authentication

### Key Labels
- `llm_isvc_name`: LLMInferenceService identifier
- `llm_isvc_role`: Component role (leader, worker, prefill)
- `llm_isvc_component`: Component type (workload, router-scheduler)

### Core Metric Examples
```promql
# Request rate
rate(kserve_vllm:request_success_total[5m])

# Error rate
100 * (1 - (sum(rate(kserve_vllm:request_success_total[5m])) / sum(rate(kserve_vllm:request_total[5m]))))

# P95 latency
histogram_quantile(0.95, rate(kserve_vllm:time_to_first_token_seconds_bucket[5m]))

# Token throughput
rate(kserve_vllm:prompt_tokens_total[5m]) + rate(kserve_vllm:generation_tokens_total[5m])
```

## Dashboard Variables

### Variables
- **datasource**: Prometheus data source selection
- **namespace**: Filter by Kubernetes namespace
  - Multi-select with "All namespaces" option for platform-wide visibility
- **llm_isvc_name**: Filter by LLMInferenceService name
  - Multi-select with "All" option for comprehensive analysis across services

### Time Ranges
Available refresh intervals: 30s, 1m, 5m, 15m, 30m, 1h, 2h, 1d
**Default**: 24-hour time window with 30-second refresh interval

## Compatibility Notes

### ODC/ODH Compatibility
The `-odc.json` dashboard versions are specifically designed for OpenShift Developer Console compatibility:

- **Panel Types**: Uses older, stable panel types (`gauge`, `graph`) instead of modern `stat` and `timeseries`
- **Schema Version**: Uses `schemaVersion: 22` for maximum compatibility
- **Datasource Format**: Uses simple `"datasource": "$datasource"` format instead of structured objects
- **Variable Structure**: Simplified templating without complex query objects
- **Field Configuration**: Uses older `fieldOptions` structure instead of modern `fieldConfig`

#### Key Differences from Standard Grafana Dashboards:
- Replaces `type: "stat"` with `type: "gauge"` for single-value displays
- Replaces `type: "timeseries"` with `type: "graph"` for time series data
- Removes advanced field configurations that may not render in ODC
- Uses basic legend and tooltip configurations

### Assumptions Made
1. **Model Identification**: Uses `llm_isvc_name` as model proxy (one model per service)
2. **Error Tracking**: Focuses on vLLM and scheduler component errors
3. **GPU Metrics**: Placeholder for DCGM exporter integration (when available)
4. **Network Metrics**: Uses Kubernetes-level metrics where AI-specific unavailable

## Installation

1. Import the JSON files into your Grafana instance
2. Configure Prometheus data source with appropriate RBAC permissions
3. Ensure LLMInferenceService monitoring is enabled (see `pkg/controller/llmisvc/monitoring.go`)
4. Verify metric collection via PodMonitor and ServiceMonitor resources

## Missing Metrics and Future Enhancements

### Identified Gaps
- **Model-specific metrics**: vLLM metrics don't include explicit model identification
- **GPU utilization**: Requires DCGM exporter for hardware-level metrics
- **AI-specific network metrics**: Currently uses general Kubernetes metrics
- **Gateway-level errors**: Need integration with ingress/routing components

### Recommendations
1. Enhance vLLM metric labeling to include model information
2. Integrate DCGM exporter for GPU monitoring
3. Add request tracing for end-to-end observability
4. Implement custom metrics for business-specific KPIs

## Troubleshooting

### Common Issues
1. **No data in panels**: Verify LLMInferenceService is running and metrics collection is enabled
2. **Permission errors**: Check ServiceAccount and ClusterRoleBinding for metrics access
3. **Missing labels**: Ensure relabeling configurations are applied in monitoring.go
4. **High cardinality**: Monitor metric storage impact with many LLMInferenceServices

### Debug Queries
```promql
# Check if metrics are being collected
{__name__=~"kserve_.*"}

# Verify label presence
kserve_vllm:request_success_total{llm_isvc_name!=""}

# Check monitoring components
up{job=~".*kserve.*"}
```