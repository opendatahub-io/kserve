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
from collections.abc import Callable
from typing import Any, Optional

from kubernetes import client, config, watch

from kserve.logging import logger

from .tls_profile import BUILTIN_PROFILES, INTERMEDIATE_PROFILE, apply_profile


def should_honor_cluster_profile(adherence: str) -> bool:
    return adherence not in ("", "LegacyAdheringComponentsOnly")


def extract_profile_spec(apiserver: dict[str, Any]) -> dict[str, Any]:
    spec = apiserver.get("spec", {})
    if not should_honor_cluster_profile(spec.get("tlsAdherence", "")):
        return INTERMEDIATE_PROFILE.copy()

    profile = spec.get("tlsSecurityProfile") or {"type": "Intermediate"}
    if profile.get("type") == "Custom" and profile.get("custom"):
        custom = profile["custom"]
        return {
            "minTLSVersion": custom.get("minTLSVersion", "VersionTLS12"),
            "ciphers": custom.get("ciphers", []),
        }
    return BUILTIN_PROFILES.get(
        profile.get("type", "Intermediate"), INTERMEDIATE_PROFILE
    ).copy()


class TLSProfileRefresher:
    """Apply and watch the OpenShift cluster TLS security profile."""

    _INITIAL_RETRY_DELAY_SECONDS = 1.0
    _MAX_RETRY_DELAY_SECONDS = 30.0

    def __init__(
        self,
        ssl_context: ssl.SSLContext,
        api_factory: Callable[[], client.CustomObjectsApi] = client.CustomObjectsApi,
        watch_factory: Callable[[], watch.Watch] = watch.Watch,
    ) -> None:
        self.ssl_context = ssl_context
        self._api_factory = api_factory
        self._watch_factory = watch_factory
        self._watch: Optional[watch.Watch] = None
        self._thread: Optional[threading.Thread] = None
        self._stopped = threading.Event()
        self._default_ciphers = tuple(
            cipher["name"] for cipher in ssl_context.get_ciphers()
        )

    def start(self) -> None:
        try:
            config.load_incluster_config()
            api = self._api_factory()
            apiserver = api.get_cluster_custom_object(
                group="config.openshift.io",
                version="v1",
                plural="apiservers",
                name="cluster",
            )
            self._apply(apiserver)
        except Exception:
            logger.exception(
                "Unable to read the OpenShift TLS profile; using Intermediate"
            )
            apply_profile(self.ssl_context, INTERMEDIATE_PROFILE, self._default_ciphers)
            api = None

        self._thread = threading.Thread(
            target=self._watch_profile,
            args=(api,),
            name="kserve-tls-profile-watcher",
            daemon=True,
        )
        self._thread.start()

    def _apply(self, apiserver: dict[str, Any]) -> None:
        profile = extract_profile_spec(apiserver)
        apply_profile(self.ssl_context, profile, self._default_ciphers)
        logger.info(
            "Applied OpenShift TLS profile with minimum version %s",
            profile["minTLSVersion"],
        )

    def _watch_profile(self, api: Optional[client.CustomObjectsApi]) -> None:
        retry_delay = self._INITIAL_RETRY_DELAY_SECONDS
        while not self._stopped.is_set():
            try:
                if api is None:
                    config.load_incluster_config()
                    api = self._api_factory()
                    apiserver = api.get_cluster_custom_object(
                        group="config.openshift.io",
                        version="v1",
                        plural="apiservers",
                        name="cluster",
                    )
                    self._apply(apiserver)

                self._watch = self._watch_factory()
                for event in self._watch.stream(
                    api.list_cluster_custom_object,
                    group="config.openshift.io",
                    version="v1",
                    plural="apiservers",
                    field_selector="metadata.name=cluster",
                    timeout_seconds=300,
                ):
                    if self._stopped.is_set():
                        return
                    if event.get("type") in ("ADDED", "MODIFIED"):
                        try:
                            self._apply(event["object"])
                        except Exception:
                            logger.exception(
                                "Unable to apply updated OpenShift TLS profile"
                            )
                retry_delay = self._INITIAL_RETRY_DELAY_SECONDS
            except Exception:
                if not self._stopped.is_set():
                    logger.exception("OpenShift TLS profile watch stopped unexpectedly")
                    api = None
                    if self._stopped.wait(retry_delay):
                        return
                    retry_delay = min(retry_delay * 2, self._MAX_RETRY_DELAY_SECONDS)

    def stop(self) -> None:
        self._stopped.set()
        if self._watch is not None:
            self._watch.stop()
        if self._thread is not None:
            self._thread.join(timeout=5)
            self._thread = None
