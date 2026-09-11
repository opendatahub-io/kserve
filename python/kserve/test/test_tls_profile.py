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

from kserve.protocol.rest.tls_profile import (
    BUILTIN_PROFILES,
    INTERMEDIATE_PROFILE,
    apply_profile,
)


def test_apply_profile_sets_openssl_ciphers():
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    apply_profile(context, INTERMEDIATE_PROFILE)
    enabled = {cipher["name"] for cipher in context.get_ciphers()}

    assert context.minimum_version == ssl.TLSVersion.TLSv1_2
    assert set(INTERMEDIATE_PROFILE["ciphers"]).issubset(enabled)


def test_apply_profile_restores_default_ciphers_for_empty_profile():
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    default_ciphers = tuple(cipher["name"] for cipher in context.get_ciphers())
    apply_profile(context, INTERMEDIATE_PROFILE, default_ciphers)
    assert len(context.get_ciphers()) < len(default_ciphers)

    apply_profile(context, BUILTIN_PROFILES["Old"], default_ciphers)

    assert {cipher["name"] for cipher in context.get_ciphers()} == set(default_ciphers)
