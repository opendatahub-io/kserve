#!/bin/bash

# Copyright 2026 The KServe Contributors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Rendered-manifest checks on the union the kserve-module operator applies.
#
# The module operator (kserve-module/) renders its own config plus the kserve
# manifests it ships in /opt/manifests, and applies them together. Two failure
# classes are invisible to per-overlay kustomize builds:
#
#   1. RBAC identity drift: a RoleBinding/ClusterRoleBinding rendered (or
#      applied) without the ServiceAccount it names. The binding then grants
#      permissions to an identity that never runs, or a pod runs as an
#      identity with no permissions (feast-module RHOAIENG-100514/100515:
#      a rendered ClusterRoleBinding bound to a ServiceAccount no overlay
#      created, controller crash-looped). Must run on the RENDERED UNION, not
#      per overlay, because the SA and the binding often come from different
#      kustomizations.
#   2. Duplicate env names on one container: the API server keeps only the
#      last entry (RHOAIENG-57579: duplicate RELATED_IMAGE_CRON_JOB).
#
# Usage:
#   hack/konflux-sim/rendered_manifest_checks.sh           # render + check
#   hack/konflux-sim/rendered_manifest_checks.sh FILE...   # check pre-rendered YAML
#
# Pre-rendered files make fault injection possible: render once, tamper with
# the copy, re-run the checks against it.
#
# Environment:
#   KUSTOMIZE  kustomize binary (default: kustomize on PATH)
#
# The structural checks run in python3 + PyYAML: yq's `as` bindings
# cross-product across documents under eval-all, which silently mis-attributes
# RBAC subjects to the wrong bindings.

set -euo pipefail

kustomize_bin=${KUSTOMIZE:-kustomize}

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT

if [ $# -gt 0 ]; then
    # '---' between the inputs for the same reason as the render loop
    # below: concatenating bare YAML files glues the last document of one
    # onto the first of the next and corrupts the parse.
    : > "$workdir/union.yaml"
    for f in "$@"; do
        if [ -s "$workdir/union.yaml" ]; then
            # a document marker must start a line - if the previous input
            # did not end with a newline, add one before the separator
            [ -n "$(tail -c 1 "$workdir/union.yaml")" ] && printf '\n' >> "$workdir/union.yaml"
            printf -- '---\n' >> "$workdir/union.yaml"
        fi
        cat -- "$f" >> "$workdir/union.yaml"
    done
    echo "== checking pre-rendered input: $*"
else
    # The union the operator applies together on OpenShift:
    #   - the module operator itself (kserve-module/config)
    #   - the kserve component (config/overlays/odh, rendered from the
    #     manifests the image ships in /opt/manifests)
    #   - the modelcache variant of the kserve component
    # odh-model-controller and modelexpress come from other repos at pinned
    # refs (see kserve-module/hack/get_kserve_manifests.sh) and are covered by
    # the operator e2e, not by this render.
    echo "== rendering union with $kustomize_bin"
    : > "$workdir/union.yaml"
    for overlay in kserve-module/config config/overlays/odh config/overlays/odh-modelcache; do
        # '---' between the renders: concatenating bare kustomize outputs
        # glues the last document of one onto the first of the next and
        # silently corrupts the parse.
        if [ -s "$workdir/union.yaml" ]; then
            # a document marker must start a line - if the previous render
            # did not end with a newline, add one before the separator
            [ -n "$(tail -c 1 "$workdir/union.yaml")" ] && printf '\n' >> "$workdir/union.yaml"
            printf -- '---\n' >> "$workdir/union.yaml"
        fi
        "$kustomize_bin" build "$overlay" >> "$workdir/union.yaml"
    done
    echo "   rendered $(grep -c '^kind:' "$workdir/union.yaml" || true) resources"
fi

python3 - "$workdir/union.yaml" <<'EOF'
import sys

import yaml

# Known-stale subjects on a clean HEAD, reported as warnings instead of
# failures so the check can land without turning every PR red (the skill's
# rule: a hard failure on day one gets the check deleted).
#
# config/overlays/odh/rbac/tls/clusterrolebinding.yaml still lists
# kserve-localmodel-controller-manager and kserve-localmodelnode-agent under
# the upstream placeholder namespace 'kserve'. The main ODH overlay does not
# deploy those controllers and never creates the ServiceAccounts; the
# modelcache overlay ships its own correct binding
# (kserve-modelcache-tls-distro-rolebinding, namespace model-serving-cache).
# The entries are dead but harmless: they grant permissions to identities
# that never exist. Cleanup: drop the two subjects from the odh overlay
# binding once the modelcache binding is confirmed as the only consumer.
KNOWN_STALE_SUBJECTS = {
    ("kserve", "kserve-localmodel-controller-manager"),
    ("kserve", "kserve-localmodelnode-agent"),
}

failures = []
warnings = []
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d and isinstance(d, dict)]

def pod_spec(doc):
    kind = doc.get("kind")
    if kind == "CronJob":
        return doc.get("spec", {}).get("jobTemplate", {}).get("spec", {}).get("template", {}).get("spec", {})
    if kind == "Pod":
        # a bare Pod's containers/serviceAccountName live on spec directly
        return doc.get("spec", {})
    return doc.get("spec", {}).get("template", {}).get("spec", {})

# Workload kinds whose pod specs are checked below. 'Pod' included: a
# rendered bare Pod must not bypass the serviceAccountName and env checks.
WORKLOAD_KINDS = ("Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob", "Pod")

# --- collect the ServiceAccounts the union itself creates ---
sa_set = set()
for d in docs:
    if d.get("kind") == "ServiceAccount":
        sa_set.add((d["metadata"].get("namespace") or "", d["metadata"]["name"]))

def sa_exists(ns, name):
    # The built-in 'default' ServiceAccount exists in every live namespace,
    # so an explicit reference to it resolves even when the union does not
    # create it (omitting serviceAccountName means the same account).
    return name == "default" or (ns, name) in sa_set

# --- check 1: every ServiceAccount subject of every binding resolves ---
subjects = 0
for d in docs:
    if d.get("kind") not in ("RoleBinding", "ClusterRoleBinding"):
        continue
    binding = f'{d["kind"]}/{d["metadata"]["name"]}'
    for s in d.get("subjects") or []:
        if s.get("kind") != "ServiceAccount":
            continue
        subjects += 1
        ns, name = s.get("namespace"), s["name"]
        if not ns:
            if d["kind"] == "RoleBinding":
                # A RoleBinding subject without a namespace resolves in the
                # binding's own namespace; only ClusterRoleBinding subjects
                # need the explicit namespace.
                ns = d["metadata"].get("namespace") or ""
            else:
                failures.append(f"❌ {binding}: subject {name} has no namespace (invalid for kind ServiceAccount)")
                continue
        if not sa_exists(ns, name):
            if (ns, name) in KNOWN_STALE_SUBJECTS:
                warnings.append(f"⚠️  {binding} binds to ServiceAccount {ns}/{name}, which the union does not create (known stale subject, see script header)")
            else:
                failures.append(f"❌ {binding} binds to ServiceAccount {ns}/{name}, which the union does not create")
print(f"   checked {subjects} ServiceAccount subjects against {len(sa_set)} created ServiceAccounts")

# --- check 2: every explicit pod serviceAccountName resolves ---
pod_sas = 0
for d in docs:
    if d.get("kind") not in WORKLOAD_KINDS:
        continue
    ns = d["metadata"].get("namespace") or ""
    sa = pod_spec(d).get("serviceAccountName")
    if not sa:
        continue  # empty means the implicit 'default' SA, always present live
    pod_sas += 1
    if not sa_exists(ns, sa):
        failures.append(f"❌ {d['kind']}/{d['metadata']['name']} in namespace {ns} runs as ServiceAccount {sa}, which the union does not create")
print(f"   checked {pod_sas} explicit pod serviceAccountNames")

# --- check 3: no duplicated env names per container ---
for d in docs:
    if d.get("kind") not in WORKLOAD_KINDS:
        continue
    spec = pod_spec(d)
    for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
        names = [e["name"] for e in (c.get("env") or []) if "name" in e]
        for dup in sorted({n for n in names if names.count(n) > 1}):
            failures.append(f"❌ {d['kind']}/{d['metadata']['name']} container {c.get('name')}: env name defined more than once: {dup}")
print("   checked env names on every rendered container")

for w in warnings:
    print(w)
for f in failures:
    print(f)
if failures:
    print(f"FAIL: rendered manifest checks found {len(failures)} problem(s) (see above)")
    sys.exit(1)
print("PASS: RBAC identities resolve and no env duplicates in the rendered union"
      + (f" ({len(warnings)} known-stale warning(s))" if warnings else ""))
EOF
