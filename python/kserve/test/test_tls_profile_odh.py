# Copyright 2026 The KServe Authors.
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

import ssl
import threading
from unittest.mock import Mock

import pytest

from kserve.protocol.rest.tls_profile import INTERMEDIATE_PROFILE
from kserve.protocol.rest.tls_profile_odh import (
    TLSProfileRefresher,
    extract_profile_spec,
)


@pytest.mark.parametrize(
    "profile_type,expected_version",
    [
        ("Old", "VersionTLS10"),
        ("Intermediate", "VersionTLS12"),
        ("Modern", "VersionTLS13"),
    ],
)
def test_extract_builtin_profile(profile_type, expected_version):
    result = extract_profile_spec(
        {
            "spec": {
                "tlsAdherence": "Strict",
                "tlsSecurityProfile": {"type": profile_type},
            }
        }
    )
    assert result["minTLSVersion"] == expected_version


def test_extract_custom_profile():
    result = extract_profile_spec(
        {
            "spec": {
                "tlsAdherence": "Strict",
                "tlsSecurityProfile": {
                    "type": "Custom",
                    "custom": {
                        "minTLSVersion": "VersionTLS12",
                        "ciphers": ["ECDHE-RSA-AES256-GCM-SHA384"],
                    },
                },
            }
        }
    )
    assert result == {
        "minTLSVersion": "VersionTLS12",
        "ciphers": ["ECDHE-RSA-AES256-GCM-SHA384"],
    }


@pytest.mark.parametrize("adherence", ["", "LegacyAdheringComponentsOnly"])
def test_non_adhering_policy_uses_intermediate(adherence):
    result = extract_profile_spec(
        {
            "spec": {
                "tlsAdherence": adherence,
                "tlsSecurityProfile": {"type": "Old"},
            }
        }
    )
    assert result == INTERMEDIATE_PROFILE


def test_refresher_reads_and_watches_profile(monkeypatch):
    api = Mock()
    api.get_cluster_custom_object.return_value = {
        "spec": {
            "tlsAdherence": "Strict",
            "tlsSecurityProfile": {"type": "Modern"},
        }
    }
    profile_watch = Mock()
    watch_started = threading.Event()
    watch_stopped = threading.Event()

    def stream(*_args, **_kwargs):
        watch_started.set()
        watch_stopped.wait(timeout=5)
        return
        yield

    profile_watch.stream.side_effect = stream
    profile_watch.stop.side_effect = watch_stopped.set
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    monkeypatch.setattr(
        "kserve.protocol.rest.tls_profile_odh.config.load_incluster_config", Mock()
    )

    refresher = TLSProfileRefresher(
        context, api_factory=lambda: api, watch_factory=lambda: profile_watch
    )
    refresher.start()
    assert watch_started.wait(timeout=5)
    refresher.stop()

    assert context.minimum_version == ssl.TLSVersion.TLSv1_3
    api.get_cluster_custom_object.assert_called_once_with(
        group="config.openshift.io",
        version="v1",
        plural="apiservers",
        name="cluster",
    )
    profile_watch.stream.assert_called_once_with(
        api.list_cluster_custom_object,
        group="config.openshift.io",
        version="v1",
        plural="apiservers",
        field_selector="metadata.name=cluster",
        timeout_seconds=300,
    )


def test_refresher_falls_back_to_intermediate(monkeypatch):
    monkeypatch.setattr(
        "kserve.protocol.rest.tls_profile_odh.config.load_incluster_config",
        Mock(side_effect=RuntimeError("not in cluster")),
    )
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)

    refresher = TLSProfileRefresher(context)
    refresher.start()
    refresher.stop()

    assert context.minimum_version == ssl.TLSVersion.TLSv1_2


def test_refresher_recovers_after_initial_discovery_failure(monkeypatch):
    load_config = Mock(side_effect=[RuntimeError("API unavailable"), None])
    monkeypatch.setattr(
        "kserve.protocol.rest.tls_profile_odh.config.load_incluster_config",
        load_config,
    )
    api = Mock()
    api.get_cluster_custom_object.return_value = {
        "spec": {
            "tlsAdherence": "Strict",
            "tlsSecurityProfile": {"type": "Modern"},
        }
    }
    watch_started = threading.Event()
    watch_stopped = threading.Event()
    profile_watch = Mock()

    def stream(*_args, **_kwargs):
        watch_started.set()
        watch_stopped.wait(timeout=5)
        return
        yield

    profile_watch.stream.side_effect = stream
    profile_watch.stop.side_effect = watch_stopped.set
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    refresher = TLSProfileRefresher(
        context, api_factory=lambda: api, watch_factory=lambda: profile_watch
    )
    refresher._INITIAL_RETRY_DELAY_SECONDS = 0.01

    refresher.start()
    assert watch_started.wait(timeout=5)
    refresher.stop()

    assert context.minimum_version == ssl.TLSVersion.TLSv1_3
    assert load_config.call_count == 2


def test_refresher_backs_off_and_recovers_after_watch_failures(monkeypatch):
    monkeypatch.setattr(
        "kserve.protocol.rest.tls_profile_odh.config.load_incluster_config", Mock()
    )
    api = Mock()
    api.get_cluster_custom_object.return_value = {
        "spec": {
            "tlsAdherence": "Strict",
            "tlsSecurityProfile": {"type": "Modern"},
        }
    }
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    refresher = TLSProfileRefresher(context, api_factory=lambda: api)
    refresher._INITIAL_RETRY_DELAY_SECONDS = 0.01
    refresher._MAX_RETRY_DELAY_SECONDS = 0.02
    wait = Mock(wraps=refresher._stopped.wait)
    refresher._stopped.wait = wait
    watch_count = 0

    def watch_factory():
        nonlocal watch_count
        watch_count += 1
        profile_watch = Mock()
        if watch_count < 3:
            profile_watch.stream.side_effect = RuntimeError("watch failed")
        else:
            profile_watch.stream.side_effect = lambda *_args, **_kwargs: (
                refresher._stopped.set() or iter(())
            )
        return profile_watch

    refresher._watch_factory = watch_factory

    refresher.start()
    refresher._thread.join(timeout=5)

    assert not refresher._thread.is_alive()
    assert watch_count == 3
    assert [call.args[0] for call in wait.call_args_list] == [0.01, 0.02]
