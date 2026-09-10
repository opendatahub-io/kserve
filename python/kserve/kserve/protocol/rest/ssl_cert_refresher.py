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

import asyncio
from collections.abc import Callable
from ssl import SSLContext
from typing import Optional

from watchfiles import Change, awatch

from kserve.logging import logger


class SSLCertRefresher:
    """Monitor TLS certificate files and reload the active SSL context."""

    _INITIAL_RETRY_DELAY_SECONDS = 1.0
    _MAX_RETRY_DELAY_SECONDS = 30.0

    def __init__(
        self,
        ssl_context: SSLContext,
        key_path: str,
        cert_path: str,
    ) -> None:
        self.ssl_context = ssl_context
        self.key_path = key_path
        self.cert_path = cert_path
        self._watch_task: Optional[asyncio.Task] = asyncio.create_task(
            self._watch_files([self.key_path, self.cert_path], self._reload_cert_chain)
        )

    def _reload_cert_chain(self, _change: Change, _file_path: str) -> None:
        logger.info("Reloading SSL certificate chain")
        self.ssl_context.load_cert_chain(self.cert_path, self.key_path)

    async def _watch_files(
        self,
        paths: list[str],
        callback: Callable[[Change, str], None],
    ) -> None:
        logger.info("Monitoring SSL certificate files: %s", paths)
        retry_delay = self._INITIAL_RETRY_DELAY_SECONDS
        while True:
            try:
                async for changes in awatch(*paths):
                    retry_delay = self._INITIAL_RETRY_DELAY_SECONDS
                    try:
                        for change, file_path in changes:
                            logger.info(
                                "SSL certificate file change detected: %s - %s",
                                change.name,
                                file_path,
                            )
                            callback(change, file_path)
                    except Exception:
                        logger.exception("Failed to reload SSL certificate chain")
                return
            except asyncio.CancelledError:
                raise
            except Exception:
                logger.exception("SSL certificate file watch stopped unexpectedly")
                await asyncio.sleep(retry_delay)
                retry_delay = min(retry_delay * 2, self._MAX_RETRY_DELAY_SECONDS)

    def stop(self) -> None:
        """Stop monitoring certificate files."""
        if self._watch_task is not None:
            self._watch_task.cancel()
            self._watch_task = None
