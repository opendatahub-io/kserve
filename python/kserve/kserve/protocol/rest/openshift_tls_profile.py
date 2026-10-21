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

import os
import ssl

from .tls_profile import TLSProfile, apply_tls_profile

TLS_MIN_VERSION_ENV = "KSERVE_TLS_MIN_VERSION"
TLS_CIPHERS_ENV = "KSERVE_TLS_CIPHERS"

_TLS_VERSIONS = {
    "VersionTLS10": ssl.TLSVersion.TLSv1,
    "VersionTLS11": ssl.TLSVersion.TLSv1_1,
    "VersionTLS12": ssl.TLSVersion.TLSv1_2,
    "VersionTLS13": ssl.TLSVersion.TLSv1_3,
}

_OPENSHIFT_TO_OPENSSL_CIPHERS = {
    "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256": "ECDHE-ECDSA-AES128-GCM-SHA256",
    "TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256": "ECDHE-RSA-AES128-GCM-SHA256",
    "TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384": "ECDHE-ECDSA-AES256-GCM-SHA384",
    "TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384": "ECDHE-RSA-AES256-GCM-SHA384",
    "TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256": "ECDHE-ECDSA-CHACHA20-POLY1305",
    "TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256": "ECDHE-RSA-CHACHA20-POLY1305",
}


class OpenShiftTLSProfileProvider:
    """Apply the OpenShift profile resolved and injected by the controller."""

    def __init__(self, ssl_context: ssl.SSLContext):
        self._ssl_context = ssl_context

    def start(self) -> None:
        min_version_name = os.environ.get(TLS_MIN_VERSION_ENV, "")
        try:
            minimum_version = _TLS_VERSIONS[min_version_name]
        except KeyError as error:
            raise ValueError(
                f"{TLS_MIN_VERSION_ENV} must name an OpenShift TLS version"
            ) from error

        cipher_names = tuple(
            cipher
            for cipher in os.environ.get(TLS_CIPHERS_ENV, "").split(",")
            if cipher
        )
        unsupported = [
            cipher
            for cipher in cipher_names
            if cipher not in _OPENSHIFT_TO_OPENSSL_CIPHERS
        ]
        if unsupported:
            raise ValueError(
                "unsupported OpenShift TLS cipher suites: " + ", ".join(unsupported)
            )

        apply_tls_profile(
            self._ssl_context,
            TLSProfile(
                minimum_version=minimum_version,
                ciphers=tuple(
                    _OPENSHIFT_TO_OPENSSL_CIPHERS[cipher] for cipher in cipher_names
                ),
            ),
        )

    def stop(self) -> None:
        pass
