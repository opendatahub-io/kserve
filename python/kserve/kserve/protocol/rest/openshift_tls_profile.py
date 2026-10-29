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

        apply_tls_profile(
            self._ssl_context,
            TLSProfile(
                minimum_version=minimum_version,
                ciphers=cipher_names,
            ),
        )

    def stop(self) -> None:
        pass
