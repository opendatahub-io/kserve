"""Helpers for kserve-module upgrade e2e tests."""

import hashlib
import importlib.util
import json
import time
from pathlib import Path

import yaml

_MANIFESTS_DIR = (
    Path(__file__).resolve().parents[3] / "docs" / "tests" / "upgrade" / "test-manifests"
)


def _parent_conftest():
    path = Path(__file__).resolve().parent.parent / "conftest.py"
    spec = importlib.util.spec_from_file_location("_e2e_parent_conftest", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


_parent = _parent_conftest()
run = _parent.run
get_cr = _parent.get_cr
is_cr_ready = _parent.is_cr_ready
operand_deployments = _parent.operand_deployments
resource_exists = _parent.resource_exists
wait_for = _parent.wait_for
wait_for_deployment = _parent.wait_for_deployment
NAMESPACE = _parent.NAMESPACE

UPGRADE_NAMESPACE = "km-upgrade-e2e"
BASELINE_CM_NAME = "km-upgrade-baseline"
MANIFESTS_DIR = _MANIFESTS_DIR
MODULE_CONTROLLER_DEPLOYMENT = "kserve-module-controller-manager"
PROBE_POD_NAME = "km-upgrade-probe"
# Image already present on ODH/OpenShift clusters; has curl for in-cluster probes.
PROBE_IMAGE = "quay.io/opendatahub/mlserver:fast"

ISVC_NAME = "sklearn-iris"
LLMISVC_NAME = "facebook-opt-125m-single"
NEW_ISVC_NAME = "sklearn-iris-post-upgrade"
NEW_LLMISVC_NAME = "facebook-opt-125m-post-upgrade"


def is_post_upgrade(pytestconfig):
    return pytestconfig.getoption("--post-upgrade")


def is_pre_upgrade(pytestconfig):
    return pytestconfig.getoption("--pre-upgrade")


def manifest_path(name):
    return MANIFESTS_DIR / name


def ensure_namespace(kubectl, namespace=UPGRADE_NAMESPACE):
    if resource_exists(kubectl, "namespace", namespace):
        run(
            [
                kubectl,
                "label",
                "namespace",
                namespace,
                "pod-security.kubernetes.io/enforce=privileged",
                "pod-security.kubernetes.io/audit=privileged",
                "pod-security.kubernetes.io/warn=privileged",
                "--overwrite",
            ],
            check=False,
        )
        return
    run([kubectl, "create", "namespace", namespace])
    run(
        [
            kubectl,
            "label",
            "namespace",
            namespace,
            "pod-security.kubernetes.io/enforce=privileged",
            "pod-security.kubernetes.io/audit=privileged",
            "pod-security.kubernetes.io/warn=privileged",
            "--overwrite",
        ]
    )


def apply_manifest(kubectl, filename, namespace=UPGRADE_NAMESPACE):
    path = manifest_path(filename)
    run([kubectl, "apply", "-n", namespace, "-f", str(path)])


def wait_for_isvc_ready(kubectl, name=ISVC_NAME, namespace=UPGRADE_NAMESPACE, timeout=600):
    def _ready():
        result = run(
            [
                kubectl,
                "get",
                "inferenceservice",
                name,
                "-n",
                namespace,
                "-o",
                "jsonpath={.status.conditions[?(@.type=='Ready')].status}",
            ],
            check=False,
        )
        assert result.stdout.strip() == "True"

    wait_for(_ready, timeout=timeout, interval=10)


def wait_for_llmisvc_ready(
    kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE, timeout=900
):
    """Wait until LLMISVC workloads are ready (matches manual upgrade scripts)."""

    def _ready():
        result = run(
            [
                kubectl,
                "get",
                "llminferenceservice",
                name,
                "-n",
                namespace,
                "-o",
                "jsonpath={.status.conditions[?(@.type=='WorkloadsReady')].status}",
            ],
            check=False,
        )
        assert result.stdout.strip() == "True"

    wait_for(_ready, timeout=timeout, interval=15)


def llmisvc_workload_service(kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE):
    svc_name = f"{name}-kserve-workload-svc"
    if resource_exists(kubectl, "service", svc_name, namespace=namespace):
        return f"http://{svc_name}.{namespace}.svc.cluster.local:8000"
    return f"http://{name}.{namespace}.svc.cluster.local"


def isvc_predictor_url(kubectl, name=ISVC_NAME, namespace=UPGRADE_NAMESPACE):
    result = run(
        [
            kubectl,
            "get",
            "inferenceservice",
            name,
            "-n",
            namespace,
            "-o",
            "jsonpath={.status.url}",
        ],
        check=False,
    )
    status_url = result.stdout.strip()
    if status_url:
        return status_url.rstrip("/")

    # RawDeployment: in-cluster predictor Service.
    return f"http://{name}-predictor.{namespace}.svc.cluster.local"


def llmisvc_inference_url(kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE):
    result = run(
        [
            kubectl,
            "get",
            "llminferenceservice",
            name,
            "-n",
            namespace,
            "-o",
            "jsonpath={.status.url}",
        ],
        check=False,
    )
    status_url = result.stdout.strip()
    if status_url:
        return status_url.rstrip("/")
    return llmisvc_workload_service(kubectl, name=name, namespace=namespace)


def _exec_curl(kubectl, namespace, resource, container, url, method="GET", data=None):
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
        "-X",
        method,
        "-w",
        "\n%{http_code}",
    ]
    if data is not None:
        cmd.extend(["-H", "Content-Type: application/json", "-d", data])
    cmd.append(url)
    result = run(cmd, timeout=120)
    lines = result.stdout.rsplit("\n", 1)
    body = lines[0] if len(lines) == 2 else result.stdout
    status = lines[1].strip() if len(lines) == 2 else "000"
    if status not in {"200", "201", "202"}:
        raise RuntimeError(f"curl {url} failed with HTTP {status}: {body}")
    return body


def run_isvc_inference(kubectl, namespace=UPGRADE_NAMESPACE, name=ISVC_NAME):
    """Verify ISVC health endpoint (same check as manual upgrade scripts)."""
    deploy = f"deploy/{name}-predictor"
    body = _exec_curl(
        kubectl,
        namespace,
        deploy,
        "kserve-container",
        "http://127.0.0.1:8080/v2/health/ready",
    )
    return hashlib.sha256(body.encode()).hexdigest()


def run_llmisvc_inference(kubectl, namespace=UPGRADE_NAMESPACE, name=LLMISVC_NAME):
    """Verify LLMISVC WorkloadsReady condition (gateway may be absent on test clusters)."""
    result = run(
        [
            kubectl,
            "get",
            "llminferenceservice",
            name,
            "-n",
            namespace,
            "-o",
            "jsonpath={.status.conditions[?(@.type=='WorkloadsReady')]}",
        ]
    )
    return hashlib.sha256(result.stdout.encode()).hexdigest()


def get_resource(kubectl, resource_type, name, namespace=None):
    cmd = [kubectl, "get", resource_type, name, "-o", "yaml"]
    if namespace:
        cmd.extend(["-n", namespace])
    result = run(cmd, check=False)
    if result.returncode != 0:
        return None
    return yaml.safe_load(result.stdout)


def deployment_pod_snapshot(kubectl, deployment, namespace=NAMESPACE):
    dep = get_resource(kubectl, "deployment", deployment, namespace=namespace)
    if dep is None:
        return {"pod_uids": [], "restart_counts": {}}

    selector = dep.get("spec", {}).get("selector", {}).get("matchLabels", {})
    if not selector:
        return {"pod_uids": [], "restart_counts": {}}

    label_parts = [f"{k}={v}" for k, v in selector.items()]
    result = run(
        [
            kubectl,
            "get",
            "pods",
            "-n",
            namespace,
            "-l",
            ",".join(label_parts),
            "-o",
            "json",
        ],
        check=False,
    )
    if result.returncode != 0:
        return {"pod_uids": [], "restart_counts": {}}

    pods = yaml.safe_load(result.stdout).get("items", [])
    pod_uids = sorted(p["metadata"]["uid"] for p in pods)
    restart_counts = {
        p["metadata"]["name"]: sum(
            cs.get("restartCount", 0) for cs in p.get("status", {}).get("containerStatuses", [])
        )
        for p in pods
    }
    return {"pod_uids": pod_uids, "restart_counts": restart_counts}


def workload_pod_snapshot(kubectl, labels, namespace=UPGRADE_NAMESPACE):
    label_parts = [f"{k}={v}" for k, v in labels.items()]
    result = run(
        [
            kubectl,
            "get",
            "pods",
            "-n",
            namespace,
            "-l",
            ",".join(label_parts),
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


def capture_kserve_baseline(kubectl):
    cr = get_cr(kubectl)
    return {
        "uid": cr["metadata"]["uid"],
        "generation": cr["metadata"].get("generation"),
        "ready": is_cr_ready(cr),
    }


# KServe/LLMISVC reconcilers whose pods must survive a module-operator image roll.
# odh-model-controller and model-serving-api are sibling components also deployed
# by kserve-module but outside the ISVC/LLMISVC serving path under test here.
OPERAND_POD_IDENTITY_DEPLOYMENTS_OCP = [
    "kserve-controller-manager",
    "llmisvc-controller-manager",
]
OPERAND_POD_IDENTITY_DEPLOYMENTS_XKS = [
    "llmisvc-controller-manager",
]


def operand_pod_identity_deployments(is_openshift):
    """Deployments whose pods must not be recreated during a module image roll."""
    if is_openshift:
        return OPERAND_POD_IDENTITY_DEPLOYMENTS_OCP
    return OPERAND_POD_IDENTITY_DEPLOYMENTS_XKS


def capture_operand_baselines(kubectl, is_openshift):
    baselines = {}
    for dep in operand_pod_identity_deployments(is_openshift):
        baselines[dep] = deployment_pod_snapshot(kubectl, dep, namespace=NAMESPACE)
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
    result = run(
        [
            kubectl,
            "get",
            "configmap",
            BASELINE_CM_NAME,
            "-n",
            namespace,
            "-o",
            "jsonpath={.data.baseline}",
        ]
    )
    return json.loads(result.stdout)


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
    """Every running operand pod must be one that existed at baseline capture.

    Uses subset semantics so a transient extra pod at baseline (e.g. during a
    rolling update) does not fail the check.
    """
    for uid in current_uids:
        assert uid in baseline_uids, (
            f"Operand pod was recreated: uid {uid} not in baseline. "
            f"baseline={baseline_uids} current={current_uids}"
        )


def crd_available(kubectl, resource_type):
    result = run([kubectl, "api-resources", "--no-headers"], check=False)
    return resource_type in result.stdout


def workloads_supported(kubectl, is_openshift):
    if not is_openshift:
        return False
    return crd_available(kubectl, "inferenceservices") and crd_available(
        kubectl, "llminferenceservices"
    )


def isvc_health_url(namespace=UPGRADE_NAMESPACE, name=ISVC_NAME):
    return (
        f"http://{name}-predictor.{namespace}.svc.cluster.local/v2/health/ready"
    )


def start_background_probe(kubectl, namespace=UPGRADE_NAMESPACE):
    """Deploy an in-cluster probe pod that polls workload endpoints until stopped.

    Runs across the manual/CI upgrade gap between pre- and post-upgrade pytest phases.
    """
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
    # Probe need not be Ready before upgrade starts; give it a moment to begin logging.
    time.sleep(5)


def verify_background_probe(kubectl, namespace=UPGRADE_NAMESPACE):
    """Assert the background probe saw no failed requests during the upgrade window."""
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


def assert_deployment_available(kubectl, name, namespace=NAMESPACE):
    """Assert deployment has at least one available replica."""
    dep = get_resource(kubectl, "deployment", name, namespace=namespace)
    assert dep is not None, f"deployment {name} not found"
    available = dep.get("status", {}).get("availableReplicas", 0)
    assert available >= 1, (
        f"deployment {name} has availableReplicas={available}, expected >= 1"
    )
