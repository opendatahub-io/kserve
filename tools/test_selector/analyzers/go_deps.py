"""Go dependency graph analysis via `go list -json`."""

from __future__ import annotations

import json
import subprocess
from dataclasses import dataclass, field
from pathlib import Path

from ..graph.depgraph import DependencyGraph


class GoListError(RuntimeError):
    """Raised when Go package discovery cannot produce a trusted mapping."""


@dataclass
class GoPackage:
    import_path: str
    dir: str
    go_files: list[str] = field(default_factory=list)
    test_go_files: list[str] = field(default_factory=list)
    imports: list[str] = field(default_factory=list)


def _parse_go_list_json(raw: str) -> list[dict]:
    """Parse concatenated JSON objects from `go list -json` output."""
    decoder = json.JSONDecoder()
    results: list[dict] = []
    pos = 0
    while pos < len(raw):
        stripped = raw[pos:].lstrip()
        if not stripped:
            break
        try:
            obj, idx = decoder.raw_decode(stripped)
            pos = pos + (len(raw[pos:]) - len(stripped)) + idx
        except json.JSONDecodeError as exc:
            raise GoListError(f"malformed go list JSON: {exc}") from exc
        if not isinstance(obj, dict):
            raise GoListError("go list JSON package entry must be an object")
        if not isinstance(obj.get("ImportPath"), str) or not obj["ImportPath"]:
            raise GoListError("go list package entry has no ImportPath")
        if not isinstance(obj.get("Dir"), str) or not obj["Dir"]:
            raise GoListError("go list package entry has no Dir")
        results.append(obj)
    if not results:
        raise GoListError("go list produced no package entries")
    return results


def _run_go_list_all(repo_root: Path) -> list[dict]:
    """Run `go list -json ./...` once and return all internal packages."""
    try:
        result = subprocess.run(
            ["go", "list", "-json", "./..."],
            cwd=repo_root,
            capture_output=True,
            text=True,
            timeout=120,
        )
    except FileNotFoundError:
        raise GoListError("'go' not found in PATH")
    except subprocess.TimeoutExpired:
        raise GoListError("'go list ./...' timed out")

    if result.returncode != 0:
        raise GoListError(
            f"'go list ./...' returned {result.returncode}: {result.stderr[:200]}"
        )

    return _parse_go_list_json(result.stdout)


def discover_entrypoints(repo_root: Path) -> list[str]:
    """Find all cmd/*/main.go entrypoints."""
    cmd_dir = repo_root / "cmd"
    if not cmd_dir.is_dir():
        return []

    entrypoints = []
    for sub in sorted(cmd_dir.iterdir()):
        if sub.is_dir() and (sub / "main.go").exists():
            entrypoints.append(f"./cmd/{sub.name}")
    return entrypoints


def build_go_dependency_info(
    repo_root: Path,
    module_prefix: str,
    entrypoint_targets: list[str],
) -> tuple[
    DependencyGraph,
    dict[str, GoPackage],
    dict[str, str],
    dict[str, list[str]],
]:
    """Build Go dependency graph for the given entrypoints.

    Runs a single `go list -json ./...` call, builds the full dependency
    graph from direct imports, then computes per-entrypoint transitive
    deps via BFS.

    Returns:
        - dep_graph: DependencyGraph of internal packages
        - packages: import_path -> GoPackage
        - file_to_package: relative file path -> import_path
        - entrypoint_packages: entrypoint target -> list of internal dep import_paths
    """
    dep_graph = DependencyGraph()
    packages: dict[str, GoPackage] = {}
    file_to_package: dict[str, str] = {}

    pkg_list = _run_go_list_all(repo_root)
    if not pkg_list:
        raise GoListError("'go list ./...' returned no packages")

    for pkg_data in pkg_list:
        import_path = pkg_data.get("ImportPath", "")
        if not import_path.startswith(module_prefix):
            continue

        pkg_dir = pkg_data.get("Dir", "")
        go_files = pkg_data.get("GoFiles", [])
        test_files = pkg_data.get("TestGoFiles", [])
        xtest_files = pkg_data.get("XTestGoFiles", [])
        imports = [
            imp for imp in pkg_data.get("Imports", []) if imp.startswith(module_prefix)
        ]

        packages[import_path] = GoPackage(
            import_path=import_path,
            dir=pkg_dir,
            go_files=go_files,
            test_go_files=test_files + xtest_files,
            imports=imports,
        )

        dep_graph.add_node(import_path)
        for imp in imports:
            dep_graph.add_edge(import_path, imp)

        for f in go_files + test_files + xtest_files:
            try:
                rel = str(Path(pkg_dir).relative_to(repo_root) / f)
            except ValueError as exc:
                raise GoListError(
                    f"go list package directory is outside repository: {pkg_dir}"
                ) from exc
            file_to_package[rel] = import_path

    if not packages:
        raise GoListError("'go list ./...' returned no internal packages")

    entrypoint_packages: dict[str, list[str]] = {}
    for target in entrypoint_targets:
        ep_import = module_prefix + target.lstrip("./")
        if ep_import in packages:
            deps = dep_graph.transitive_dependencies({ep_import})
            entrypoint_packages[target] = sorted(deps)
        else:
            entrypoint_packages[target] = []

    return dep_graph, packages, file_to_package, entrypoint_packages


def get_module_prefix(repo_root: Path) -> str:
    """Read the module path from go.mod."""
    go_mod = repo_root / "go.mod"
    for line in go_mod.read_text().splitlines():
        line = line.strip()
        if line.startswith("module "):
            return line.split()[1] + "/"
    raise SystemExit("Could not parse module path from go.mod")
