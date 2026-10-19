"""Unit tests for upgrade helper functions."""

import json

import pytest

from upgrade.utils import (
    UPGRADE_IMAGE_ENV,
    _parse_probe_records,
    _probe_failures_after_baseline,
    assert_operand_pods_not_recreated,
    assert_pod_uids_unchanged,
    assert_restart_counts_not_increased,
    run_isvc_inference,
    verify_module_controller_rolled,
    wait_for_probe_baseline,
    wait_for_workload_pods_stable,
)


class TestParseProbeRecords:
    def test_parses_valid_records(self):
        logs = "\n".join(
            [
                '{"ts":"t1","target":"isvc","status":0,"ok":false}',
                '{"ts":"t2","target":"isvc","status":200,"ok":true}',
            ]
        )
        records, malformed = _parse_probe_records(logs)
        assert len(records) == 2
        assert malformed == []

    def test_malformed_records_are_reported(self):
        logs = '{"ts":"t1","target":"isvc","status":0000,"ok":false}'
        records, malformed = _parse_probe_records(logs)
        assert records == []
        assert malformed == [logs]

    def test_ignores_blank_lines(self):
        records, malformed = _parse_probe_records("\n\n")
        assert records == []
        assert malformed == []


class TestAssertRestartCountsNotIncreased:
    def test_missing_pod_fails_when_required(self):
        with pytest.raises(AssertionError, match="no longer present"):
            assert_restart_counts_not_increased(
                {"pod-a": 0},
                {},
                require_baseline_pods=True,
            )

    def test_missing_pod_allowed_for_module_controller(self):
        assert_restart_counts_not_increased(
            {"pod-a": 0},
            {},
            require_baseline_pods=False,
        )

    def test_restart_increase_fails(self):
        with pytest.raises(AssertionError, match="restart count increased"):
            assert_restart_counts_not_increased({"pod-a": 0}, {"pod-a": 1})


class TestWaitForProbeBaseline:
    def test_waits_until_first_success(self, monkeypatch):
        responses = [
            '{"ts":"t1","target":"isvc","status":0,"ok":false}\n',
            '{"ts":"t2","target":"isvc","status":200,"ok":true}\n',
        ]
        call_count = 0

        def fake_run(_cmd, check=False):
            nonlocal call_count
            stdout = responses[min(call_count, len(responses) - 1)]
            call_count += 1
            return type(
                "R", (), {"returncode": 0, "stdout": stdout, "stderr": ""}
            )()

        def fake_wait_for(fn, timeout=60, interval=5):
            for _ in range(3):
                try:
                    return fn()
                except AssertionError:
                    continue
            return fn()

        monkeypatch.setattr("upgrade.utils.run", fake_run)
        monkeypatch.setattr("upgrade.utils.wait_for", fake_wait_for)

        wait_for_probe_baseline("kubectl")
        assert call_count >= 2

    def test_rejects_malformed_records(self, monkeypatch):
        monkeypatch.setattr(
            "upgrade.utils.run",
            lambda *_a, **_k: type(
                "R",
                (),
                {
                    "returncode": 0,
                    "stdout": '{"ts":"t1","target":"isvc","status":000,"ok":false}',
                    "stderr": "",
                },
            )(),
        )
        monkeypatch.setattr("upgrade.utils.wait_for", lambda fn, **_kw: fn())

        with pytest.raises(AssertionError, match="malformed"):
            wait_for_probe_baseline("kubectl")


class TestProbeFailuresAfterBaseline:
    def test_ignores_failures_before_first_success(self):
        records = [
            {"ok": False, "status": 0},
            {"ok": True, "status": 200},
            {"ok": False, "status": 503},
        ]
        baseline_idx, failures = _probe_failures_after_baseline(records)
        assert baseline_idx == 1
        assert len(failures) == 1
        assert failures[0]["status"] == 503

    def test_no_baseline_returns_none(self):
        records = [{"ok": False, "status": 0}]
        baseline_idx, failures = _probe_failures_after_baseline(records)
        assert baseline_idx is None
        assert failures == []


class TestRunIsvcInference:
    def test_hashes_valid_predictions(self, monkeypatch):
        monkeypatch.setattr(
            "upgrade.utils._exec_curl",
            lambda *_args, **_kwargs: json.dumps(
                {"outputs": [{"data": [1, 1]}]}
            ),
        )
        monkeypatch.setattr(
            "upgrade.utils.manifest_path",
            lambda _name: type(
                "P",
                (),
                {
                    "read_text": lambda self: json.dumps(
                        {"instances": [[0, 0, 0, 0], [0, 0, 0, 0]]}
                    )
                },
            )(),
        )

        digest = run_isvc_inference("kubectl")
        assert len(digest) == 64

    def test_rejects_missing_predictions(self, monkeypatch):
        monkeypatch.setattr(
            "upgrade.utils._exec_curl",
            lambda *_args, **_kwargs: json.dumps({"status": "ok"}),
        )
        monkeypatch.setattr(
            "upgrade.utils.manifest_path",
            lambda _name: type(
                "P",
                (),
                {
                    "read_text": lambda self: json.dumps(
                        {"instances": [[0, 0, 0, 0], [0, 0, 0, 0]]}
                    )
                },
            )(),
        )

        with pytest.raises(AssertionError, match="missing predictions"):
            run_isvc_inference("kubectl")


class TestVerifyModuleControllerRolled:
    def test_requires_upgrade_image_env(self, monkeypatch):
        monkeypatch.delenv(UPGRADE_IMAGE_ENV, raising=False)
        with pytest.raises(AssertionError, match=UPGRADE_IMAGE_ENV):
            verify_module_controller_rolled("kubectl", {"module_controller": {"pod_uids": ["a"]}})

    def test_fails_when_pod_uids_unchanged(self, monkeypatch):
        monkeypatch.setenv(UPGRADE_IMAGE_ENV, "kserve-module-controller:e2e")
        monkeypatch.setattr("upgrade.utils.wait_for_deployment", lambda *_a, **_k: None)
        monkeypatch.setattr(
            "upgrade.utils.get_module_controller_image",
            lambda *_a, **_k: "kserve-module-controller:e2e",
        )
        monkeypatch.setattr(
            "upgrade.utils.deployment_pod_snapshot",
            lambda *_a, **_k: {"pod_uids": ["uid-a"], "restart_counts": {}},
        )

        baseline = {"module_controller": {"pod_uids": ["uid-a"], "restart_counts": {}}}
        with pytest.raises(AssertionError, match="UIDs unchanged"):
            verify_module_controller_rolled("kubectl", baseline)


class TestWaitForWorkloadPodsStable:
    def test_returns_when_uids_stop_changing(self, monkeypatch):
        calls = iter(
            [
                {"pod_uids": ["a"], "pod_names": ["p1"], "restart_counts": {}},
                {"pod_uids": ["a"], "pod_names": ["p1"], "restart_counts": {}},
                {"pod_uids": ["a"], "pod_names": ["p1"], "restart_counts": {}},
            ]
        )

        def fake_snapshot(*_args, **_kwargs):
            return next(calls)

        monkeypatch.setattr("upgrade.utils.workload_pod_snapshot", fake_snapshot)
        monkeypatch.setattr("upgrade.utils.time.sleep", lambda _s: None)

        snap = wait_for_workload_pods_stable(
            "kubectl", {"app": "x"}, stable_seconds=0, interval=0
        )
        assert snap["pod_uids"] == ["a"]


class TestAssertPodUidsUnchanged:
    def test_includes_pod_names_in_error(self):
        with pytest.raises(AssertionError, match="pod-a"):
            assert_pod_uids_unchanged(
                ["uid-a"],
                ["uid-b"],
                baseline_names=["pod-a"],
                current_names=["pod-b"],
            )


class TestAssertOperandPodsNotRecreated:
    def test_missing_baseline_pod_fails(self):
        with pytest.raises(AssertionError, match="Operand pod UIDs changed"):
            assert_operand_pods_not_recreated(["uid-a", "uid-b"], ["uid-a"])

    def test_new_pod_fails(self):
        with pytest.raises(AssertionError):
            assert_operand_pods_not_recreated(["uid-a"], ["uid-b"])

    def test_matching_sets_pass(self):
        assert_operand_pods_not_recreated(["uid-a"], ["uid-a"])
