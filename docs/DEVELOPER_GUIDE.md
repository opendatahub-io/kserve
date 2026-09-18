# Developer Guide

Please review the [KServe Developer Guide](https://github.com/kserve/website/blob/main/docs/developer-guide/index.md) docs.

## Tracing headers

KServe's Python REST data plane automatically emits the active OpenTelemetry trace ID on every HTTP response unless tracing is disabled with `OTEL_SDK_DISABLED=true`. By default, the runtime surfaces a W3C Trace Context compliant `traceparent` header (and `tracestate` when available) so that downstream systems can correlate logs and spans. Operators can override the header names by setting the `TRACE_RESPONSE_HEADER_NAME` and `TRACE_RESPONSE_TRACESTATE_HEADER_NAME` environment variables on the serving runtime.

Instrumentation and span exporting are configured independently. Set `OTEL_TRACES_EXPORTER=otlp` to export spans using the standard `OTEL_EXPORTER_OTLP_*` settings, or set `OTEL_TRACES_EXPORTER=console` for local diagnostics. Set `OTEL_TRACES_EXPORTER=none` to keep instrumentation and context propagation enabled without exporting spans. If no exporter or OTLP endpoint is configured, KServe does not create an exporter. Sampling follows the standard `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG` settings. Set `OTEL_SDK_DISABLED=true` to disable tracing instrumentation entirely.

## LLMInferenceService tracing egress policy

In distribution builds, the LLMInferenceService controller can create a
workload-side egress NetworkPolicy for OTLP tracing. The policy is opt-in and
does not change when tracing is configured unless the following annotation is
set to `"true"`:

```yaml
metadata:
  annotations:
    serving.kserve.io/enable-tracing-egress-network-policy: "true"
```

When enabled, the controller creates a per-service `-otlp-egress` policy owned
by the LLMInferenceService. The policy allows DNS, same-namespace traffic, and
cross-namespace OTLP traffic when the exporter endpoint names an existing
cluster-local Service with a pod selector. When the controller's Kubernetes API
endpoint is an IP address, API access is limited to that IP and configured
port. The cross-namespace peer selects that Service's pods and namespace on
the endpoint port; the port is taken from the endpoint or defaults to TCP port
4317.
Deleting the LLMInferenceService lets Kubernetes garbage collection remove the
owned policy.

External hosts, IP addresses, unsupported or missing URL schemes, missing
Services, and cross-namespace Services without pod selectors do not receive an
OTLP egress rule; the controller logs this limitation. The workload-side
egress policy is separate from collector-side ingress policy and does not use
`MONITORING_NAMESPACE`. If the controller API endpoint is hostname-based, no
API egress rule is generated; provide a separate policy if workloads require
Kubernetes API access in that configuration.
