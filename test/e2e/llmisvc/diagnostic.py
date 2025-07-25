# Copyright 2025 The KServe Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#    http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

import itertools
from datetime import datetime
import pytest
from kubernetes import client, config, dynamic
from kubernetes.client import api_client
from kubernetes.client.exceptions import ApiException
from kserve import KServeClient, V1alpha1LLMInferenceService, constants


def print_all_events_table(namespace: str, max_events: int = 50):
    """
    Print the most recent `max_events` events in `namespace` as a nice table.
    """
    core = client.CoreV1Api()

    try:
        evs = core.list_namespaced_event(namespace=namespace).items

        if not evs:
            print("ℹ️ # No events found in namespace", namespace)
            return

        evs = sorted(
            evs, key=lambda e: e.last_timestamp or e.first_timestamp, reverse=True
        )[:max_events]

        header = f"{'TIME':<25} {'NAMESPACE':<12} {'SOURCE':<20} {'TYPE':<8} {'REASON':<20} MESSAGE"
        print(header)
        print("-" * len(header))

        for ev in evs:
            ts = ev.last_timestamp or ev.first_timestamp
            ts_str = (
                ts.strftime("%Y-%m-%d %H:%M:%S")
                if isinstance(ts, datetime)
                else str(ts)
            )
            src = f"{ev.source.component or ''}/{ev.source.host or ''}".strip("/")
            msg = (ev.message or "").replace("\n", " ")
            print(
                f"{ts_str:<25} {ev.metadata.namespace:<12} {src:<20} {ev.type or '':<8} "
                f"{ev.reason or '':<20} {msg}"
            )

    except Exception as e:
        print(f"# ❌ failed to list events: {e}")


def kinds_matching_by_labels(namespace: str, labels, api_kinds):
    """
    List all namespaced objects in `namespace` matching `labels`
    whose kind is in `api_kinds`.

    :param namespace: kube namespace to search
    :param labels: either a dict of {k: v} or a raw selector string
    :param api_kinds: an iterable of Resource.kind strings to include
    :return: list of Unstructured objects
    """
    config.load_kube_config()
    dyn = dynamic.DynamicClient(api_client.ApiClient())

    selector = (
        ",".join(f"{k}={v}" for k, v in labels.items())
        if isinstance(labels, dict)
        else labels
    )

    all_resources = itertools.chain.from_iterable(dyn.resources)

    found = []
    for rsrc in all_resources:
        if not rsrc.namespaced or "list" not in rsrc.verbs:
            continue
        if rsrc.kind not in api_kinds:
            continue

        try:
            resp = rsrc.get(namespace=namespace, label_selector=selector)
        except ApiException:
            continue

        items = getattr(resp, "items", [])
        found.extend(items)

    return found
