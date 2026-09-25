from __future__ import annotations

import os
from pathlib import Path
import shutil
import subprocess
import sys


GATEKEEPER_DIR = Path(__file__).parents[1]


def test_gatekeeper_image_packages_trusted_nonroot_runtime() -> None:
    dockerfile = (GATEKEEPER_DIR / "Dockerfile").read_text()
    requirements = (GATEKEEPER_DIR / "requirements-gatekeeper.txt").read_text()

    assert (
        "FROM golang:1.26.7-bookworm@sha256:"
        "e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"
        in dockerfile
    )
    assert "registry.ci.openshift.org" not in dockerfile
    assert "python3.11" in dockerfile
    for tool in ("git", "jq", "curl", "openssl"):
        assert tool in dockerfile
    assert "USER 1001" in dockerfile
    assert 'PYTHONPATH="/opt/test-selector:/opt/python"' in dockerfile
    assert 'PYTHONSAFEPATH="1"' in dockerfile
    assert 'GOTOOLCHAIN="local"' in dockerfile
    assert "/opt/test-selector/test_selector/" in dockerfile
    assert "ENV PATH" not in dockerfile
    assert "ln -sf /usr/bin/python3.11 /usr/local/bin/python" in dockerfile
    assert "test_selector.gatekeeper.evaluate" in dockerfile
    assert "test_selector.gatekeeper.dispatch" not in dockerfile
    assert "PRIVATE_KEY" not in dockerfile
    assert "GITHUB_TOKEN" not in dockerfile

    assert "PyYAML==6.0.2" in requirements
    assert "PyYAML>=" not in requirements
    assert "cryptography==44.0.3" in requirements
    assert "# CI self-test support for the Python 3.11 pytest suite." in requirements
    assert "pytest==8.3.5" in requirements
    assert "jsonschema==4.23.0" in requirements


def test_staged_evaluator_and_dispatcher_modules_have_runnable_cli(
    tmp_path: Path,
) -> None:
    """Smoke-test the package at the same import root used by the image."""
    staged_root = tmp_path / "test-selector"
    staged_package = staged_root / "test_selector"
    source_package = GATEKEEPER_DIR.parent
    staged_package.mkdir(parents=True)
    for source in (
        "__init__.py",
        "__main__.py",
        "cli.py",
        "config.json",
        "config_loader.py",
    ):
        shutil.copy2(source_package / source, staged_package / source)
    for directory in (
        "analyzers",
        "graph",
        "mapping",
        "output",
        "selector",
        "gatekeeper",
    ):
        shutil.copytree(source_package / directory, staged_package / directory)

    env = os.environ.copy()
    env["PYTHONPATH"] = str(staged_root)
    malicious = tmp_path / "test_selector" / "gatekeeper"
    malicious.mkdir(parents=True)
    (malicious.parent / "__init__.py").write_text("raise SystemExit('shadowed')\n")
    (malicious / "__init__.py").write_text("")
    for module in ("evaluate", "dispatch"):
        (malicious / f"{module}.py").write_text("raise SystemExit('shadowed')\n")

    env["PYTHONSAFEPATH"] = "1"
    for module in (
        "test_selector.gatekeeper.evaluate",
        "test_selector.gatekeeper.dispatch",
    ):
        result = subprocess.run(
            [sys.executable, "-m", module, "--help"],
            capture_output=True,
            text=True,
            cwd=tmp_path,
            env=env,
            check=False,
        )
        assert result.returncode == 0, result.stderr

    crypto = subprocess.run(
        [
            sys.executable,
            "-c",
            "from test_selector.gatekeeper import dispatch; "
            "assert dispatch.hashes is not None",
        ],
        capture_output=True,
        text=True,
        cwd=tmp_path,
        env=env,
        check=False,
    )
    assert crypto.returncode == 0, crypto.stderr
