# Developer Guide

Please review the [KServe Developer Guide](https://github.com/kserve/website/blob/main/docs/developer-guide/index.md) docs.

## Tracing headers

KServe's Python REST data plane automatically emits the active OpenTelemetry trace ID on every HTTP response unless tracing is disabled with `OTEL_SDK_DISABLED=true`. By default, the runtime surfaces a W3C Trace Context compliant `traceparent` header (and `tracestate` when available) so that downstream systems can correlate logs and spans. Operators can override the header names by setting the `TRACE_RESPONSE_HEADER_NAME` and `TRACE_RESPONSE_TRACESTATE_HEADER_NAME` environment variables on the serving runtime.

Instrumentation and span exporting are configured independently. Set `OTEL_TRACES_EXPORTER=otlp` to export spans using the standard `OTEL_EXPORTER_OTLP_*` settings, or set `OTEL_TRACES_EXPORTER=console` for local diagnostics. Set `OTEL_TRACES_EXPORTER=none` to keep instrumentation and context propagation enabled without exporting spans. If no exporter or OTLP endpoint is configured, KServe does not create an exporter. Sampling follows the standard `OTEL_TRACES_SAMPLER` and `OTEL_TRACES_SAMPLER_ARG` settings. Set `OTEL_SDK_DISABLED=true` to disable tracing instrumentation entirely.

## LLMInferenceService tracing egress policy

In distribution builds, the LLMInferenceService controller can create an
opt-in, workload-side egress NetworkPolicy for OTLP tracing. Tracing alone does
not enable the policy; set the following annotation to `"true"`:

```yaml
metadata:
  annotations:
    serving.kserve.io/enable-tracing-egress-network-policy: "true"
```

When enabled, the controller creates a per-service `-otlp-egress` policy owned
by the LLMInferenceService. The policy allows DNS, same-namespace traffic, and
cross-namespace OTLP traffic when the exporter endpoint names an existing
cluster-local Service with a pod selector. The cross-namespace peer selects
that Service's pods and namespace on the endpoint port; the port is taken from
the endpoint or defaults to TCP port 4317.

The policy also retains TCP ports 443 and 6443 as a compatibility rule. Port
443 is required for HTTPS model downloads, and NetworkPolicy processing of
connections to the Kubernetes API Service can vary with Service-IP DNAT and
the network plugin. This broad rule is intentional for the opt-in policy until
the target platform can guarantee a narrower API-server rule. Services that
need stricter external egress should add a separate policy or use PVC/OCI model
sources.
Deleting the LLMInferenceService lets Kubernetes garbage collection remove the
owned policy.

External hosts, IP addresses, unsupported or missing URL schemes, missing
Services, and cross-namespace Services without pod selectors do not receive an
OTLP egress rule; the controller logs this limitation. The workload-side
egress policy is separate from collector-side ingress policy and does not
configure collector ingress or use `MONITORING_NAMESPACE`. The collector-side
policy remains a platform responsibility. This opt-in policy is the current
compatibility shape; a future platform-specific policy may replace the broad
443/6443 rule once the collector and API-server networking contract is
standardized.
