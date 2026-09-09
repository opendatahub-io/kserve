"""Helpers for kserve-module upgrade e2e tests."""

import hashlib
import importlib.util
import json
import time
from pathlib import Path

import yaml


def _load_e2e_conftest():
    # upgrade/ has its own conftest.py, so `import conftest` would load that
    # package and create a circular import. Load the parent e2e conftest by path.
    path = Path(__file__).resolve().parent.parent / "conftest.py"
    spec = importlib.util.spec_from_file_location("_e2e_conftest", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_e2e = _load_e2e_conftest()

run = _e2e.run
create_kserve_cr = _e2e.create_kserve_cr
get_cr = _e2e.get_cr
get_jsonpath = _e2e.get_jsonpath
get_resource = _e2e.get_resource
is_cr_ready = _e2e.is_cr_ready
operand_deployments = _e2e.operand_deployments
resource_exists = _e2e.resource_exists
wait_for = _e2e.wait_for
wait_for_deployment = _e2e.wait_for_deployment
NAMESPACE = _e2e.NAMESPACE
MODULE_CONTROLLER_DEPLOYMENT = _e2e.OPERATOR_DEPLOYMENT

MANIFESTS_DIR = (
    Path(__file__).resolve().parents[3] / "docs" / "tests" / "upgrade" / "test-manifests"
)

UPGRADE_NAMESPACE = "km-upgrade-e2e"
BASELINE_CM_NAME = "km-upgrade-baseline"
PROBE_POD_NAME = "km-upgrade-probe"
PROBE_IMAGE = "quay.io/opendatahub/mlserver:fast"

ISVC_NAME = "sklearn-iris"
LLMISVC_NAME = "facebook-opt-125m-single"
NEW_ISVC_NAME = "sklearn-iris-post-upgrade"
NEW_LLMISVC_NAME = "facebook-opt-125m-post-upgrade"

# Operand reconcilers that must keep the same pod identity across a module image roll.
OPERAND_POD_IDENTITY_NAMES = frozenset(
    {"kserve-controller-manager", "llmisvc-controller-manager"}
)


def is_post_upgrade(pytestconfig):
    return pytestconfig.getoption("--post-upgrade")


def is_pre_upgrade(pytestconfig):
    return pytestconfig.getoption("--pre-upgrade")


def manifest_path(name):
    return MANIFESTS_DIR / name


def ensure_namespace(kubectl, namespace=UPGRADE_NAMESPACE):
    labels = [
        "pod-security.kubernetes.io/enforce=privileged",
        "pod-security.kubernetes.io/audit=privileged",
        "pod-security.kubernetes.io/warn=privileged",
    ]
    if not resource_exists(kubectl, "namespace", namespace):
        run([kubectl, "create", "namespace", namespace])
    run(
        [kubectl, "label", "namespace", namespace, *labels, "--overwrite"],
        check=False,
    )


def apply_manifest(kubectl, filename, namespace=UPGRADE_NAMESPACE):
    run([kubectl, "apply", "-n", namespace, "-f", str(manifest_path(filename))])


def wait_for_isvc_ready(kubectl, name=ISVC_NAME, namespace=UPGRADE_NAMESPACE, timeout=600):
    def _ready():
        status = get_jsonpath(
            kubectl,
            "inferenceservice",
            name,
            "{.status.conditions[?(@.type=='Ready')].status}",
            namespace=namespace,
        )
        assert status == "True"

    wait_for(_ready, timeout=timeout, interval=10)


def wait_for_llmisvc_ready(
    kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE, timeout=900
):
    def _ready():
        status = get_jsonpath(
            kubectl,
            "llminferenceservice",
            name,
            "{.status.conditions[?(@.type=='WorkloadsReady')].status}",
            namespace=namespace,
        )
        assert status == "True"

    wait_for(_ready, timeout=timeout, interval=15)


def _exec_curl(kubectl, namespace, resource, container, url):
    cmd = [
        kubectl,
        "exec",
        "-n",
        namespace,
        resource,
        "-c",
        container,
        "--",
        "curl",
        "-sk",
        "--connect-timeout",
        "10",
        "--max-time",
        "60",
        "-w",
        "\n%{http_code}",
        url,
    ]
    result = run(cmd, timeout=120)
    lines = result.stdout.rsplit("\n", 1)
    body = lines[0] if len(lines) == 2 else result.stdout
    status = lines[1].strip() if len(lines) == 2 else "000"
    if status not in {"200", "201", "202"}:
        raise RuntimeError(f"curl {url} failed with HTTP {status}: {body}")
    return body


def run_isvc_inference(kubectl, namespace=UPGRADE_NAMESPACE, name=ISVC_NAME):
    body = _exec_curl(
        kubectl,
        namespace,
        f"deploy/{name}-predictor",
        "kserve-container",
        "http://127.0.0.1:8080/v2/health/ready",
    )
    return hashlib.sha256(body.encode()).hexdigest()


def check_llmisvc_workloads_ready(kubectl, namespace=UPGRADE_NAMESPACE, name=LLMISVC_NAME):
    """Return a stable hash of the WorkloadsReady condition (readiness, not inference)."""
    condition = get_jsonpath(
        kubectl,
        "llminferenceservice",
        name,
        "{.status.conditions[?(@.type=='WorkloadsReady')]}",
        namespace=namespace,
    )
    return hashlib.sha256(condition.encode()).hexdigest()


def _pod_snapshot(kubectl, namespace, labels):
    if not labels:
        return {"pod_uids": [], "restart_counts": {}}

    label_selector = ",".join(f"{k}={v}" for k, v in labels.items())
    result = run(
        [
            kubectl,
            "get",
            "pods",
            "-n",
            namespace,
            "-l",
            label_selector,
            "-o",
            "json",
        ],
        check=False,
    )
    if result.returncode != 0:
        return {"pod_uids": [], "restart_counts": {}}

    pods = yaml.safe_load(result.stdout).get("items", [])
    return {
        "pod_uids": sorted(p["metadata"]["uid"] for p in pods),
        "restart_counts": {
            p["metadata"]["name"]: sum(
                cs.get("restartCount", 0)
                for cs in p.get("status", {}).get("containerStatuses", [])
            )
            for p in pods
        },
    }


def deployment_pod_snapshot(kubectl, deployment, namespace=NAMESPACE):
    dep = get_resource(kubectl, "deployment", deployment, namespace=namespace)
    if dep is None:
        return {"pod_uids": [], "restart_counts": {}}
    labels = dep.get("spec", {}).get("selector", {}).get("matchLabels", {})
    return _pod_snapshot(kubectl, namespace, labels)


def workload_pod_snapshot(kubectl, labels, namespace=UPGRADE_NAMESPACE):
    return _pod_snapshot(kubectl, namespace, labels)


def capture_kserve_baseline(kubectl):
    cr = get_cr(kubectl)
    return {
        "uid": cr["metadata"]["uid"],
        "generation": cr["metadata"].get("generation"),
        "ready": is_cr_ready(cr),
    }


def operand_pod_identity_deployments(is_openshift):
    return [d for d in operand_deployments(is_openshift) if d in OPERAND_POD_IDENTITY_NAMES]


def capture_operand_baselines(kubectl, is_openshift):
    baselines = {
        dep: deployment_pod_snapshot(kubectl, dep, namespace=NAMESPACE)
        for dep in operand_pod_identity_deployments(is_openshift)
    }
    baselines[MODULE_CONTROLLER_DEPLOYMENT] = deployment_pod_snapshot(
        kubectl, MODULE_CONTROLLER_DEPLOYMENT, namespace=NAMESPACE
    )
    return baselines


def capture_isvc_baseline(kubectl, name=ISVC_NAME, namespace=UPGRADE_NAMESPACE):
    isvc = get_resource(kubectl, "inferenceservice", name, namespace=namespace)
    pods = workload_pod_snapshot(
        kubectl,
        {"serving.kserve.io/inferenceservice": name},
        namespace=namespace,
    )
    return {
        "uid": isvc["metadata"]["uid"],
        "generation": isvc["metadata"].get("generation"),
        "observed_generation": isvc.get("status", {}).get("observedGeneration"),
        "url": isvc.get("status", {}).get("url", ""),
        "pod_uids": pods["pod_uids"],
        "restart_counts": pods["restart_counts"],
    }


def capture_llmisvc_baseline(kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE):
    llmisvc = get_resource(kubectl, "llminferenceservice", name, namespace=namespace)
    pods = workload_pod_snapshot(
        kubectl,
        {"app.kubernetes.io/name": name},
        namespace=namespace,
    )
    if not pods["pod_uids"]:
        pods = workload_pod_snapshot(
            kubectl,
            {"serving.kserve.io/llminferenceservice": name},
            namespace=namespace,
        )
    return {
        "uid": llmisvc["metadata"]["uid"],
        "generation": llmisvc["metadata"].get("generation"),
        "observed_generation": llmisvc.get("status", {}).get("observedGeneration"),
        "url": llmisvc.get("status", {}).get("url", ""),
        "pod_uids": pods["pod_uids"],
        "restart_counts": pods["restart_counts"],
    }


def build_baseline(
    kubectl,
    is_openshift,
    isvc_hash=None,
    llmisvc_hash=None,
    include_workloads=True,
):
    baseline = {
        "kserve": capture_kserve_baseline(kubectl),
        "operands": capture_operand_baselines(kubectl, is_openshift),
        "workloads": {},
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    if include_workloads:
        baseline["workloads"][ISVC_NAME] = capture_isvc_baseline(kubectl)
        baseline["workloads"][ISVC_NAME]["inference_hash"] = isvc_hash
        baseline["workloads"][LLMISVC_NAME] = capture_llmisvc_baseline(kubectl)
        baseline["workloads"][LLMISVC_NAME]["inference_hash"] = llmisvc_hash
    return baseline


def save_baseline(kubectl, baseline, namespace=UPGRADE_NAMESPACE):
    cm_yaml = yaml.safe_dump(
        {
            "apiVersion": "v1",
            "kind": "ConfigMap",
            "metadata": {"name": BASELINE_CM_NAME, "namespace": namespace},
            "data": {"baseline": json.dumps(baseline)},
        }
    )
    run([kubectl, "apply", "-f", "-"], input_text=cm_yaml)


def load_baseline(kubectl, namespace=UPGRADE_NAMESPACE):
    if not resource_exists(kubectl, "configmap", BASELINE_CM_NAME, namespace=namespace):
        raise AssertionError(
            f"Baseline ConfigMap {BASELINE_CM_NAME} not found in {namespace}. "
            "Run pre-upgrade tests first."
        )
    baseline = get_jsonpath(
        kubectl,
        "configmap",
        BASELINE_CM_NAME,
        "{.data.baseline}",
        namespace=namespace,
    )
    return json.loads(baseline)


def assert_restart_counts_not_increased(baseline_counts, current_counts):
    for pod, count in baseline_counts.items():
        current = current_counts.get(pod, count)
        assert current <= count, (
            f"Pod {pod} restart count increased from {count} to {current}"
        )


def assert_pod_uids_unchanged(baseline_uids, current_uids):
    assert baseline_uids == current_uids, (
        f"Pod UIDs changed: baseline={baseline_uids} current={current_uids}"
    )


def assert_operand_pods_not_recreated(baseline_uids, current_uids):
    for uid in current_uids:
        assert uid in baseline_uids, (
            f"Operand pod was recreated: uid {uid} not in baseline. "
            f"baseline={baseline_uids} current={current_uids}"
        )


def workloads_supported(kubectl, is_openshift):
    if not is_openshift:
        return False
    resources = run([kubectl, "api-resources", "--no-headers"], check=False).stdout
    return "inferenceservices" in resources and "llminferenceservices" in resources


def isvc_health_url(namespace=UPGRADE_NAMESPACE, name=ISVC_NAME):
    return f"http://{name}-predictor.{namespace}.svc.cluster.local/v2/health/ready"


def start_background_probe(kubectl, namespace=UPGRADE_NAMESPACE):
    run(
        [kubectl, "delete", "pod", PROBE_POD_NAME, "-n", namespace, "--ignore-not-found"],
        check=False,
    )
    isvc_url = isvc_health_url(namespace=namespace)
    probe_script = f"""#!/bin/sh
set -u
while true; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  code=$(curl -sk -o /dev/null -w '%{{http_code}}' --connect-timeout 5 --max-time 10 "{isvc_url}" 2>/dev/null || echo 0)
  if [ "$code" -ge 200 ] 2>/dev/null && [ "$code" -lt 300 ] 2>/dev/null; then
    ok=true
  else
    ok=false
  fi
  printf '{{"ts":"%s","target":"isvc","status":%s,"ok":%s}}\\n' "$ts" "$code" "$ok"
  sleep 2
done
"""
    pod = {
        "apiVersion": "v1",
        "kind": "Pod",
        "metadata": {
            "name": PROBE_POD_NAME,
            "namespace": namespace,
            "labels": {"app": PROBE_POD_NAME},
        },
        "spec": {
            "restartPolicy": "Never",
            "containers": [
                {
                    "name": "probe",
                    "image": PROBE_IMAGE,
                    "command": ["/bin/sh", "-c"],
                    "args": [probe_script],
                }
            ],
        },
    }
    run([kubectl, "apply", "-f", "-"], input_text=yaml.safe_dump(pod))

    def _probe_running():
        phase = get_jsonpath(
            kubectl, "pod", PROBE_POD_NAME, "{.status.phase}", namespace=namespace
        )
        assert phase == "Running"

    wait_for(_probe_running, timeout=60, interval=2)


def verify_background_probe(kubectl, namespace=UPGRADE_NAMESPACE):
    result = run(
        [kubectl, "logs", PROBE_POD_NAME, "-n", namespace],
        check=False,
    )
    if result.returncode != 0:
        raise AssertionError(
            f"Could not read probe logs: {result.stderr}. "
            "Ensure pre-upgrade started the probe pod before the module roll."
        )
    failures = []
    for line in result.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("ok") is False:
            failures.append(record)
    assert not failures, (
        f"Background probe detected {len(failures)} failed request(s); "
        f"first failures: {failures[:5]}"
    )
