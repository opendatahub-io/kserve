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

import pytest

from kserve.protocol.rest.openshift_tls_profile import OpenShiftTLSProfileProvider
from kserve.protocol.rest.tls_profile import load_tls_profile_provider


def test_openshift_provider_applies_injected_profile(monkeypatch):
    monkeypatch.setenv("KSERVE_TLS_MIN_VERSION", "VersionTLS12")
    monkeypatch.setenv(
        "KSERVE_TLS_CIPHERS",
        "ECDHE-RSA-AES128-GCM-SHA256,ECDHE-RSA-AES256-GCM-SHA384",
    )
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)

    OpenShiftTLSProfileProvider(context).start()

    assert context.minimum_version == ssl.TLSVersion.TLSv1_2
    enabled = {cipher["name"] for cipher in context.get_ciphers()}
    assert "ECDHE-RSA-AES128-GCM-SHA256" in enabled
    assert "ECDHE-RSA-AES256-GCM-SHA384" in enabled


def test_openshift_provider_rejects_unknown_values(monkeypatch):
    monkeypatch.setenv("KSERVE_TLS_MIN_VERSION", "not-a-version")
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)

    with pytest.raises(ValueError, match="KSERVE_TLS_MIN_VERSION"):
        OpenShiftTLSProfileProvider(context).start()


def test_openshift_provider_is_registered():
    provider = load_tls_profile_provider(
        "openshift", ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    )

    assert isinstance(provider, OpenShiftTLSProfileProvider)
