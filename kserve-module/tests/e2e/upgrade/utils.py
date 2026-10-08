"""Helpers for kserve-module upgrade e2e tests."""

import hashlib
import importlib.util
import json
import os
import time
from pathlib import Path

import yaml


def _load_e2e_conftest():
    # upgrade/ has its own conftest.py, so `import conftest` would load that
    # package and create a circular import. Load the parent e2e conftest by path.
    # __file__ -> kserve-module/tests/e2e/upgrade/utils.py
    # parent.parent -> kserve-module/tests/e2e/conftest.py
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

MODULE_CONTROLLER_CONTAINER = "manager"
UPGRADE_IMAGE_ENV = "KSERVE_MODULE_UPGRADE_IMAGE"
PROBE_BASELINE_TIMEOUT = 120


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


def apply_mlserver_runtime(kubectl, namespace=UPGRADE_NAMESPACE):
    """Install MLServer ServingRuntime, preferring the cluster OpenShift Template."""
    template_check = run(
        [
            kubectl,
            "get",
            "template",
            "mlserver-runtime-template",
            "-n",
            "opendatahub",
        ],
        check=False,
    )
    if template_check.returncode == 0:
        processed = run(
            [kubectl, "process", "-n", "opendatahub", "mlserver-runtime-template"],
            timeout=120,
        )
        run(
            [kubectl, "apply", "-n", namespace, "-f", "-"],
            input_text=processed.stdout,
            timeout=120,
        )
        return
    apply_manifest(kubectl, "mlserver-runtime.yaml", namespace=namespace)


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


def wait_for_workload_pods_stable(
    kubectl,
    labels,
    namespace=UPGRADE_NAMESPACE,
    timeout=180,
    stable_seconds=20,
    interval=5,
):
    """Wait until the pod UID set for a label selector stops changing."""
    deadline = time.time() + timeout
    last_uids = None
    stable_until = 0.0

    while time.time() < deadline:
        snap = workload_pod_snapshot(kubectl, labels, namespace=namespace)
        uids = tuple(snap["pod_uids"])
        if not uids:
            stable_until = 0.0
            last_uids = None
        elif uids == last_uids:
            if stable_until == 0.0:
                stable_until = time.time() + stable_seconds
            elif time.time() >= stable_until:
                return snap
        else:
            stable_until = 0.0
            last_uids = uids
        time.sleep(interval)

    raise TimeoutError(
        f"Workload pods did not stabilize within {timeout}s "
        f"(labels={labels}, last_uids={last_uids})"
    )


def wait_for_upgrade_workloads_settled(kubectl, namespace=UPGRADE_NAMESPACE):
    """Wait for pre-upgrade ISVC/LLMISVC pods to finish rolling after module image roll."""
    wait_for_isvc_ready(kubectl, name=ISVC_NAME, namespace=namespace)
    wait_for_llmisvc_ready(kubectl, name=LLMISVC_NAME, namespace=namespace)
    wait_for_workload_pods_stable(
        kubectl,
        {"serving.kserve.io/inferenceservice": ISVC_NAME},
        namespace=namespace,
    )
    wait_for_workload_pods_stable(
        kubectl,
        {"app.kubernetes.io/name": LLMISVC_NAME},
        namespace=namespace,
    )


def expected_upgrade_image():
    return os.environ.get(UPGRADE_IMAGE_ENV, "").strip()


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
        "-X",
        method,
        "--connect-timeout",
        "10",
        "--max-time",
        "60",
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


def _v2_infer_payload(instances_json):
    data = json.loads(instances_json)
    instances = data["instances"]
    flat = [float(value) for row in instances for value in row]
    rows = len(instances)
    cols = len(instances[0]) if instances else 0
    return json.dumps(
        {
            "inputs": [
                {
                    "name": "input-0",
                    "shape": [rows, cols],
                    "datatype": "FP32",
                    "data": flat,
                }
            ]
        }
    )


def _predictions_from_infer_response(body):
    parsed = json.loads(body)
    predictions = parsed.get("predictions")
    if predictions is not None:
        return predictions
    outputs = parsed.get("outputs")
    if not outputs:
        return None
    data = outputs[0].get("data")
    if data is None:
        return None
    if data and isinstance(data[0], list):
        return [row[0] for row in data]
    return data


def run_isvc_inference(kubectl, namespace=UPGRADE_NAMESPACE, name=ISVC_NAME):
    """Run a real sklearn predict request and return a hash of the predictions."""
    instances_json = manifest_path("sklearn-iris-input.json").read_text()
    payload = _v2_infer_payload(instances_json)
    body = _exec_curl(
        kubectl,
        namespace,
        f"deploy/{name}-predictor",
        "kserve-container",
        f"http://127.0.0.1:8080/v2/models/{name}/infer",
        method="POST",
        data=payload,
    )
    predictions = _predictions_from_infer_response(body)
    assert predictions is not None, f"ISVC predict response missing predictions: {body}"
    assert len(predictions) == 2, f"expected 2 predictions, got {predictions}"
    for prediction in predictions:
        label = int(prediction)
        assert 0 <= label <= 2, f"unexpected iris class label: {prediction}"
    canonical = json.dumps(predictions, sort_keys=True)
    return hashlib.sha256(canonical.encode()).hexdigest()


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
        "pod_names": sorted(p["metadata"]["name"] for p in pods),
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


def get_module_controller_image(kubectl, namespace=NAMESPACE):
    dep = get_resource(kubectl, "deployment", MODULE_CONTROLLER_DEPLOYMENT, namespace=namespace)
    assert dep is not None, f"Deployment {MODULE_CONTROLLER_DEPLOYMENT} not found in {namespace}"
    containers = dep["spec"]["template"]["spec"]["containers"]
    container = next(
        (c for c in containers if c["name"] == MODULE_CONTROLLER_CONTAINER),
        containers[0],
    )
    image = container.get("image", "").strip()
    assert image, f"Module controller deployment has no image in {namespace}"
    return image


def capture_module_controller_baseline(kubectl):
    pods = deployment_pod_snapshot(kubectl, MODULE_CONTROLLER_DEPLOYMENT, namespace=NAMESPACE)
    return {
        "image": get_module_controller_image(kubectl),
        "pod_uids": pods["pod_uids"],
        "pod_names": pods.get("pod_names", []),
        "restart_counts": pods["restart_counts"],
    }


def verify_module_controller_rolled(kubectl, baseline):
    """Fail clearly when the module-controller image roll did not take effect."""
    expected_image = expected_upgrade_image()
    assert expected_image, (
        f"{UPGRADE_IMAGE_ENV} must be set to the upgrade image ref before post-upgrade tests "
        "(set it to the same value passed as E2E_IMG to e2e-roll-kserve-module)"
    )

    wait_for_deployment(kubectl, MODULE_CONTROLLER_DEPLOYMENT)
    current_image = get_module_controller_image(kubectl)
    assert current_image == expected_image, (
        "Module controller deployment image was not updated to the upgrade image: "
        f"expected={expected_image} actual={current_image}"
    )

    module_baseline = baseline.get("module_controller")
    if not module_baseline:
        module_baseline = baseline.get("operands", {}).get(MODULE_CONTROLLER_DEPLOYMENT, {})
    baseline_uids = module_baseline.get("pod_uids", [])
    assert baseline_uids, (
        "No module-controller pod UIDs in baseline; capture baseline before the roll"
    )

    current = deployment_pod_snapshot(kubectl, MODULE_CONTROLLER_DEPLOYMENT, namespace=NAMESPACE)
    current_uids = current["pod_uids"]
    assert current_uids, "No module-controller pods found after the image roll"
    assert set(baseline_uids) != set(current_uids), (
        "Module controller pod UIDs unchanged after image roll; "
        f"rollout may not have occurred (baseline={baseline_uids} current={current_uids})"
    )

    assert_restart_counts_not_increased(
        module_baseline.get("restart_counts", {}),
        current["restart_counts"],
        require_baseline_pods=False,
    )


def capture_operand_baselines(kubectl, is_openshift):
    baselines = {
        dep: deployment_pod_snapshot(kubectl, dep, namespace=NAMESPACE)
        for dep in operand_pod_identity_deployments(is_openshift)
    }
    return baselines


def capture_isvc_baseline(kubectl, name=ISVC_NAME, namespace=UPGRADE_NAMESPACE):
    isvc = get_resource(kubectl, "inferenceservice", name, namespace=namespace)
    assert isvc is not None, (
        f"InferenceService {name} not found in {namespace}; cannot capture baseline"
    )
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
        "pod_names": pods["pod_names"],
        "restart_counts": pods["restart_counts"],
    }


def capture_llmisvc_baseline(kubectl, name=LLMISVC_NAME, namespace=UPGRADE_NAMESPACE):
    llmisvc = get_resource(kubectl, "llminferenceservice", name, namespace=namespace)
    assert llmisvc is not None, (
        f"LLMInferenceService {name} not found in {namespace}; cannot capture baseline"
    )
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
        "pod_names": pods["pod_names"],
        "restart_counts": pods["restart_counts"],
    }


def build_baseline(
    kubectl,
    is_openshift,
    isvc_hash=None,
    llmisvc_workloads_ready_hash=None,
    include_workloads=True,
):
    baseline = {
        "kserve": capture_kserve_baseline(kubectl),
        "operands": capture_operand_baselines(kubectl, is_openshift),
        "module_controller": capture_module_controller_baseline(kubectl),
        "workloads": {},
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    if include_workloads:
        baseline["workloads"][ISVC_NAME] = capture_isvc_baseline(kubectl)
        baseline["workloads"][ISVC_NAME]["inference_hash"] = isvc_hash
        baseline["workloads"][LLMISVC_NAME] = capture_llmisvc_baseline(kubectl)
        baseline["workloads"][LLMISVC_NAME]["workloads_ready_hash"] = (
            llmisvc_workloads_ready_hash
        )
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


def assert_restart_counts_not_increased(
    baseline_counts, current_counts, require_baseline_pods=True
):
    for pod, count in baseline_counts.items():
        if pod not in current_counts:
            assert not require_baseline_pods, (
                f"Pod {pod} from baseline is no longer present"
            )
            continue
        current = current_counts[pod]
        assert current <= count, (
            f"Pod {pod} restart count increased from {count} to {current}"
        )


def assert_pod_uids_unchanged(
    baseline_uids, current_uids, baseline_names=None, current_names=None
):
    assert baseline_uids == current_uids, (
        "Pod UIDs changed: "
        f"baseline={baseline_uids} ({baseline_names or 'n/a'}) "
        f"current={current_uids} ({current_names or 'n/a'})"
    )


def assert_operand_pods_not_recreated(baseline_uids, current_uids):
    assert set(baseline_uids) == set(current_uids), (
        f"Operand pod UIDs changed: baseline={baseline_uids} current={current_uids}"
    )


def _force_delete(kubectl, kind, name, namespace=UPGRADE_NAMESPACE):
    """Delete a namespaced resource even when serving finalizers stall."""
    if not resource_exists(kubectl, kind, name, namespace=namespace):
        return
    run(
        [
            kubectl,
            "patch",
            kind,
            name,
            "-n",
            namespace,
            "-p",
            '{"metadata":{"finalizers":[]}}',
            "--type=merge",
        ],
        check=False,
        timeout=60,
    )
    run(
        [
            kubectl,
            "delete",
            kind,
            name,
            "-n",
            namespace,
            "--ignore-not-found",
            "--wait=false",
        ],
        check=False,
        timeout=60,
    )


def cleanup_upgrade_workloads(kubectl, namespace=UPGRADE_NAMESPACE):
    """Remove stale upgrade test resources so reruns start from a clean slate."""
    for kind, names in [
        ("inferenceservice", [ISVC_NAME, NEW_ISVC_NAME]),
        ("llminferenceservice", [LLMISVC_NAME, NEW_LLMISVC_NAME]),
    ]:
        for name in names:
            _force_delete(kubectl, kind, name, namespace=namespace)
    for kind, names in [
        ("servingruntime", ["mlserver-runtime"]),
        ("configmap", [BASELINE_CM_NAME]),
        ("pod", [PROBE_POD_NAME]),
    ]:
        for name in names:
            run(
                [kubectl, "delete", kind, name, "-n", namespace, "--ignore-not-found"],
                check=False,
                timeout=60,
            )


def cleanup_post_upgrade_workloads(kubectl, namespace=UPGRADE_NAMESPACE):
    """Delete Part B workloads so post-upgrade reruns do not collide on fixed names."""
    for kind, name in [
        ("inferenceservice", NEW_ISVC_NAME),
        ("llminferenceservice", NEW_LLMISVC_NAME),
    ]:
        _force_delete(kubectl, kind, name, namespace=namespace)


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
  code=$(curl -sk -o /dev/null -w '%{{http_code}}' --connect-timeout 5 --max-time 10 "{isvc_url}" 2>/dev/null || true)
  case "$code" in
    ''|*[!0-9]*) code=0 ;;
    *)
      code=$((10#$code))
      ;;
  esac
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
    wait_for_probe_baseline(kubectl, namespace=namespace)


def wait_for_probe_baseline(kubectl, namespace=UPGRADE_NAMESPACE, timeout=PROBE_BASELINE_TIMEOUT):
    """Wait until the background probe records at least one successful request."""

    def _assert_baseline():
        result = run(
            [kubectl, "logs", PROBE_POD_NAME, "-n", namespace, "--tail=50"],
            check=False,
        )
        assert result.returncode == 0, f"Could not read probe logs: {result.stderr}"
        records, malformed = _parse_probe_records(result.stdout)
        assert not malformed, (
            f"Background probe emitted malformed record(s): {malformed[:3]}"
        )
        baseline_idx, _ = _probe_failures_after_baseline(records)
        assert baseline_idx is not None, "Probe has not recorded ok:true yet"

    wait_for(_assert_baseline, timeout=timeout, interval=2)


def _parse_probe_records(log_text):
    records = []
    malformed = []
    for line in log_text.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            records.append(json.loads(line))
        except json.JSONDecodeError:
            malformed.append(line)
    return records, malformed


def _probe_failures_after_baseline(records):
    baseline_idx = next(
        (idx for idx, record in enumerate(records) if record.get("ok") is True),
        None,
    )
    if baseline_idx is None:
        return None, []
    failures = [
        record
        for record in records[baseline_idx + 1 :]
        if record.get("ok") is False
    ]
    return baseline_idx, failures


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
    records, malformed = _parse_probe_records(result.stdout)
    assert not malformed, (
        f"Background probe emitted {len(malformed)} malformed record(s); "
        f"first entries: {malformed[:5]}"
    )
    assert records, "Background probe produced no valid records"

    baseline_idx, failures = _probe_failures_after_baseline(records)
    assert baseline_idx is not None, (
        "Background probe never recorded a successful baseline before upgrade"
    )
    assert not failures, (
        f"Background probe detected {len(failures)} failed request(s) after baseline; "
        f"first failures: {failures[:5]}"
    )
