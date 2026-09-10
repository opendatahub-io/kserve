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
from collections.abc import Sequence
from typing import Any

INTERMEDIATE_PROFILE: dict[str, Any] = {
    "minTLSVersion": "VersionTLS12",
    "ciphers": [
        "ECDHE-RSA-AES128-GCM-SHA256",
        "ECDHE-ECDSA-AES128-GCM-SHA256",
        "ECDHE-RSA-AES256-GCM-SHA384",
        "ECDHE-ECDSA-AES256-GCM-SHA384",
    ],
}

BUILTIN_PROFILES: dict[str, dict[str, Any]] = {
    "Old": {"minTLSVersion": "VersionTLS10", "ciphers": []},
    "Intermediate": INTERMEDIATE_PROFILE,
    "Modern": {"minTLSVersion": "VersionTLS13", "ciphers": []},
}

VERSION_MAP: dict[str, ssl.TLSVersion] = {
    "VersionTLS10": ssl.TLSVersion.TLSv1,
    "VersionTLS11": ssl.TLSVersion.TLSv1_1,
    "VersionTLS12": ssl.TLSVersion.TLSv1_2,
    "VersionTLS13": ssl.TLSVersion.TLSv1_3,
}


def apply_profile(
    ssl_context: ssl.SSLContext,
    spec: dict[str, Any],
    default_ciphers: Sequence[str] | None = None,
) -> None:
    ssl_context.minimum_version = VERSION_MAP.get(
        spec.get("minTLSVersion"), ssl.TLSVersion.TLSv1_2
    )
    ciphers = spec.get("ciphers", [])
    if ciphers and ssl_context.minimum_version < ssl.TLSVersion.TLSv1_3:
        ssl_context.set_ciphers(":".join(ciphers))
    elif not ciphers and default_ciphers is not None:
        ssl_context.set_ciphers(":".join(default_ciphers))
