import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


HELPER = Path(__file__).with_name("review_snapshot.py")


class ReviewSnapshotTest(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self._git("init", "-b", "main")
        self._git("config", "user.email", "test@example.com")
        self._git("config", "user.name", "Test User")
        self._write_contract({"version": "v1.0.0", "status": "draft"})
        self._write("tracked.txt", "initial\n")
        self._git("add", "interface-contract.json", "tracked.txt")
        self._git("commit", "-m", "initial")
        self.counter = 0

    def tearDown(self):
        self.temp_dir.cleanup()

    def _git(self, *args):
        return subprocess.run(
            ["git", *args],
            cwd=self.repo,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )

    def _write(self, relative, content):
        path = self.repo / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def _write_contract(self, value):
        (self.repo / "interface-contract.json").write_text(
            json.dumps(value, ensure_ascii=False) + "\n", encoding="utf-8"
        )

    def _json_file(self, prefix, value):
        self.counter += 1
        path = self.root / f"{prefix}-{self.counter}.json"
        path.write_text(json.dumps(value, ensure_ascii=False), encoding="utf-8")
        return path

    def _raw_json_file(self, prefix, value):
        self.counter += 1
        path = self.root / f"{prefix}-{self.counter}.json"
        path.write_text(value, encoding="utf-8")
        return path

    def _run(self, *args):
        return subprocess.run(
            [sys.executable, str(HELPER), *map(str, args)],
            cwd=self.repo,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )

    def _baseline(self, scope, contract=True):
        scope_file = self._json_file("scope", scope)
        output = self.root / f"baseline-{self.counter}.json"
        args = ["baseline", "--scope", scope_file, "--output", "-"]
        if contract:
            args.extend(["--contract", "interface-contract.json"])
        result = self._run(*args)
        self.assertEqual(result.returncode, 0, result.stderr)
        self._persist_evidence(output, result.stdout)
        return scope_file, output

    def _snapshot(self, scope_file, baseline, contract=True):
        self.counter += 1
        output = self.root / f"snapshot-{self.counter}.json"
        args = [
            "snapshot",
            "--scope",
            scope_file,
            "--baseline",
            baseline,
            "--output",
            "-",
        ]
        if contract:
            args.extend(["--contract", "interface-contract.json"])
        result = self._run(*args)
        if result.returncode == 0:
            self._persist_evidence(output, result.stdout)
        return result, output

    def _persist_evidence(self, path, value):
        with path.open("xb") as stream:
            stream.write(value.encode("utf-8"))

    def _load_snapshot(self, output):
        return json.loads(output.read_text(encoding="utf-8"))

    def _worktree_entries(self):
        return sorted(
            str(path.relative_to(self.repo))
            for path in self.repo.rglob("*")
            if ".git" not in path.relative_to(self.repo).parts
        )

    def _configure_ignore(self, source, pattern):
        if source == "gitignore":
            self._write(".gitignore", pattern + "\n")
            self._git("add", ".gitignore")
            self._git("commit", "-m", "add ignore rule")
        elif source == "info-exclude":
            (self.repo / ".git" / "info" / "exclude").write_text(
                pattern + "\n",
                encoding="utf-8",
            )
        elif source == "global-exclude":
            excludes = self.root / "global-excludes"
            excludes.write_text(pattern + "\n", encoding="utf-8")
            self._git("config", "core.excludesFile", str(excludes))
        else:
            self.fail(f"unknown ignore source: {source}")

    def _assert_ignored_prefix_changes_snapshot_id(self, source):
        self._configure_ignore(source, "ignored/")
        self._write("ignored/fixture.txt", "before\n")
        self._git("check-ignore", "ignored/fixture.txt")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["ignored/"]})

        first, first_output = self._snapshot(scope, baseline)
        self._write("ignored/fixture.txt", "after\n")
        second, second_output = self._snapshot(scope, baseline)

        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertNotEqual(
            self._load_snapshot(first_output)["id"],
            self._load_snapshot(second_output)["id"],
        )
        self.assertEqual(
            [item["path"] for item in self._load_snapshot(second_output)["manifest"]["files"]],
            ["ignored/fixture.txt"],
        )

    def test_new_in_scope_untracked_file_is_included(self):
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        self._write("src/new.txt", "new\n")

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertRegex(self._load_snapshot(output)["id"], r"^[0-9a-f]{64}$")
        files = self._load_snapshot(output)["manifest"]["files"]
        self.assertEqual(files, [{
            "index": {"entries": [], "type": "absent"},
            "mode": "100644",
            "path": "src/new.txt",
            "sha256": hashlib.sha256(b"new\n").hexdigest(),
            "type": "regular",
        }])

    def test_gitignore_hidden_prefix_file_changes_snapshot_id(self):
        self._assert_ignored_prefix_changes_snapshot_id("gitignore")

    def test_info_exclude_hidden_prefix_file_changes_snapshot_id(self):
        self._assert_ignored_prefix_changes_snapshot_id("info-exclude")

    def test_global_exclude_hidden_prefix_file_changes_snapshot_id(self):
        self._assert_ignored_prefix_changes_snapshot_id("global-exclude")

    def test_ignore_rule_switch_cannot_hide_in_scope_file(self):
        self._write("ignored/fixture.txt", "stable\n")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["ignored/"]})
        first, first_output = self._snapshot(scope, baseline)
        self.assertEqual(first.returncode, 0, first.stderr)

        self._configure_ignore("info-exclude", "ignored/")
        self._git("check-ignore", "ignored/fixture.txt")
        second, second_output = self._snapshot(scope, baseline)

        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(
            self._load_snapshot(first_output)["id"],
            self._load_snapshot(second_output)["id"],
        )
        self.assertEqual(
            [item["path"] for item in self._load_snapshot(second_output)["manifest"]["files"]],
            ["ignored/fixture.txt"],
        )

    def test_out_of_scope_ignored_file_does_not_affect_snapshot(self):
        self._configure_ignore("info-exclude", "ignored-outside/")
        self._write("ignored-outside/local.txt", "before\n")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        first, first_output = self._snapshot(scope, baseline)
        self.assertEqual(first.returncode, 0, first.stderr)

        self._write("ignored-outside/local.txt", "after\n")
        second, second_output = self._snapshot(scope, baseline)

        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(
            self._load_snapshot(first_output)["id"],
            self._load_snapshot(second_output)["id"],
        )

    def test_ignore_rule_switch_cannot_hide_out_of_scope_baseline_file(self):
        self._write("outside-visible/local.txt", "stable\n")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})

        self._configure_ignore("info-exclude", "outside-visible/")
        self._git("check-ignore", "outside-visible/local.txt")
        result, _ = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 2)
        self.assertIn("out-of-scope candidate fingerprint changed", result.stderr)

    def test_existing_out_of_scope_untracked_is_excluded_and_stable(self):
        self._write("notes/local.txt", "keep\n")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})

        first, first_output = self._snapshot(scope, baseline)
        second, second_output = self._snapshot(scope, baseline)

        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(first.stdout, second.stdout)
        self.assertEqual(
            self._load_snapshot(first_output)["manifest"],
            self._load_snapshot(second_output)["manifest"],
        )
        paths = [item["path"] for item in self._load_snapshot(first_output)["manifest"]["files"]]
        self.assertNotIn("notes/local.txt", paths)

    def test_new_out_of_scope_candidate_blocks_snapshot(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        self._write("outside.txt", "new\n")

        result, _ = self._snapshot(scope, baseline)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("out-of-scope", result.stderr)

    def test_in_scope_file_committed_after_baseline_is_included(self):
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        self._write("src/committed.txt", "committed after baseline\n")
        self._git("add", "src/committed.txt")
        self._git("commit", "-m", "commit in-scope file after baseline")

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        paths = [item["path"] for item in self._load_snapshot(output)["manifest"]["files"]]
        self.assertEqual(paths, ["src/committed.txt"])

    def test_out_of_scope_file_committed_after_baseline_blocks_snapshot(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        self._write("outside-committed.txt", "committed after baseline\n")
        self._git("add", "outside-committed.txt")
        self._git("commit", "-m", "commit out-of-scope file after baseline")

        result, _ = self._snapshot(scope, baseline)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("out-of-scope candidate fingerprint changed", result.stderr)

    def test_assume_unchanged_tracked_file_under_prefix_changes_snapshot_id(self):
        self._write("src/assumed.txt", "before\n")
        self._git("add", "src/assumed.txt")
        self._git("commit", "-m", "add assume-unchanged fixture")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        first, first_output = self._snapshot(scope, baseline)
        self.assertEqual(first.returncode, 0, first.stderr)

        self._git("update-index", "--assume-unchanged", "src/assumed.txt")
        try:
            self._write("src/assumed.txt", "after\n")
            second, second_output = self._snapshot(scope, baseline)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertNotEqual(
                self._load_snapshot(first_output)["id"],
                self._load_snapshot(second_output)["id"],
            )
        finally:
            self._git("update-index", "--no-assume-unchanged", "src/assumed.txt")

    def test_skip_worktree_tracked_file_under_prefix_changes_snapshot_id(self):
        self._write("src/skipped.txt", "before\n")
        self._git("add", "src/skipped.txt")
        self._git("commit", "-m", "add skip-worktree fixture")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        first, first_output = self._snapshot(scope, baseline)
        self.assertEqual(first.returncode, 0, first.stderr)

        self._git("update-index", "--skip-worktree", "src/skipped.txt")
        try:
            self._write("src/skipped.txt", "after\n")
            second, second_output = self._snapshot(scope, baseline)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertNotEqual(
                self._load_snapshot(first_output)["id"],
                self._load_snapshot(second_output)["id"],
            )
        finally:
            self._git("update-index", "--no-skip-worktree", "src/skipped.txt")

    def test_assume_unchanged_tracked_file_outside_scope_blocks_snapshot(self):
        self._write("outside-assumed.txt", "before\n")
        self._git("add", "outside-assumed.txt")
        self._git("commit", "-m", "add outside assume-unchanged fixture")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})

        self._git("update-index", "--assume-unchanged", "outside-assumed.txt")
        try:
            self._write("outside-assumed.txt", "after\n")
            result, _ = self._snapshot(scope, baseline)
            self.assertEqual(result.returncode, 2)
            self.assertIn("out-of-scope candidate fingerprint changed", result.stderr)
        finally:
            self._git("update-index", "--no-assume-unchanged", "outside-assumed.txt")

    def test_skip_worktree_tracked_file_outside_scope_blocks_snapshot(self):
        self._write("outside-skipped.txt", "before\n")
        self._git("add", "outside-skipped.txt")
        self._git("commit", "-m", "add outside skip-worktree fixture")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})

        self._git("update-index", "--skip-worktree", "outside-skipped.txt")
        try:
            self._write("outside-skipped.txt", "after\n")
            result, _ = self._snapshot(scope, baseline)
            self.assertEqual(result.returncode, 2)
            self.assertIn("out-of-scope candidate fingerprint changed", result.stderr)
        finally:
            self._git("update-index", "--no-skip-worktree", "outside-skipped.txt")

    def test_staged_only_in_scope_content_changes_snapshot_id(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        clean_result, clean_output = self._snapshot(scope, baseline)
        self.assertEqual(clean_result.returncode, 0, clean_result.stderr)

        self._write("tracked.txt", "staged content\n")
        self._git("add", "tracked.txt")
        self._write("tracked.txt", "initial\n")
        staged_result, staged_output = self._snapshot(scope, baseline)

        self.assertEqual(staged_result.returncode, 0, staged_result.stderr)
        self.assertNotEqual(
            self._load_snapshot(clean_output)["id"],
            self._load_snapshot(staged_output)["id"],
        )
        fingerprint = self._load_snapshot(staged_output)["manifest"]["files"][0]
        self.assertEqual(fingerprint["sha256"], hashlib.sha256(b"initial\n").hexdigest())
        self.assertEqual(fingerprint["index"]["type"], "entries")
        index_entry = fingerprint["index"]["entries"][0]
        self.assertEqual(index_entry["mode"], "100644")
        self.assertEqual(index_entry["oid"], self._git("rev-parse", ":tracked.txt").stdout.strip())
        self.assertEqual(index_entry["stage"], 0)

    def test_staged_only_out_of_scope_content_blocks_snapshot(self):
        scope, baseline = self._baseline({"paths": ["missing.txt"], "prefixes": []})
        self._write("tracked.txt", "staged content\n")
        self._git("add", "tracked.txt")
        self._write("tracked.txt", "initial\n")

        result, _ = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 2)
        self.assertIn("out-of-scope candidate fingerprint changed", result.stderr)

    def test_v1_baseline_evidence_is_rejected(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        value = json.loads(baseline.read_text(encoding="utf-8"))
        value["schema"] = "porsche-web-review-baseline-v1"
        baseline.write_text(
            json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
            + "\n",
            encoding="utf-8",
        )

        result, _ = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 2)
        self.assertIn("unsupported baseline schema", result.stderr)

    def test_deleted_out_of_scope_candidate_blocks_snapshot(self):
        outside = self._write("outside.txt", "old\n")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        outside.unlink()

        result, _ = self._snapshot(scope, baseline)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("out-of-scope", result.stderr)

    def test_modified_out_of_scope_candidate_blocks_snapshot(self):
        self._write("outside.txt", "old\n")
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        self._write("outside.txt", "changed\n")

        result, _ = self._snapshot(scope, baseline)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("out-of-scope", result.stderr)

    def test_exact_absent_path_is_encoded(self):
        scope, baseline = self._baseline({"paths": ["missing.txt"], "prefixes": []})

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._load_snapshot(output)["manifest"]["files"], [{
            "index": {"entries": [], "type": "absent"},
            "mode": None,
            "path": "missing.txt",
            "sha256": None,
            "type": "absent",
        }])

    def test_tracked_modified_deleted_and_rename_endpoints_are_included(self):
        self._write("src/modified.txt", "before\n")
        self._write("src/deleted.txt", "before\n")
        self._write("src/old.txt", "rename\n")
        self._git("add", "src")
        self._git("commit", "-m", "add source files")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        self._write("src/modified.txt", "after\n")
        (self.repo / "src/deleted.txt").unlink()
        self._git("mv", "src/old.txt", "src/new.txt")

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        files = {item["path"]: item for item in self._load_snapshot(output)["manifest"]["files"]}
        self.assertEqual(files["src/modified.txt"]["type"], "regular")
        self.assertEqual(files["src/deleted.txt"]["type"], "absent")
        self.assertEqual(files["src/old.txt"]["type"], "absent")
        self.assertEqual(files["src/new.txt"]["type"], "regular")

    def test_tracked_copy_includes_source_and_destination(self):
        self._write("src/original.txt", "copy me\n")
        self._git("add", "src/original.txt")
        self._git("commit", "-m", "add copy source")
        scope, baseline = self._baseline({"paths": [], "prefixes": ["src/"]})
        self._write("src/copied.txt", "copy me\n")
        self._git("add", "src/copied.txt")

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        paths = [item["path"] for item in self._load_snapshot(output)["manifest"]["files"]]
        self.assertEqual(paths, ["src/copied.txt", "src/original.txt"])

    def test_symlink_hashes_utf8_link_target(self):
        scope, baseline = self._baseline({"paths": ["link.txt"], "prefixes": []})
        os.symlink("目标.txt", self.repo / "link.txt")

        result, output = self._snapshot(scope, baseline)

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._load_snapshot(output)["manifest"]["files"], [{
            "index": {"entries": [], "type": "absent"},
            "mode": "120000",
            "path": "link.txt",
            "sha256": hashlib.sha256("目标.txt".encode("utf-8")).hexdigest(),
            "type": "symlink",
        }])

    def test_identical_contents_generate_identical_id(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})

        first, _ = self._snapshot(scope, baseline)
        second, _ = self._snapshot(scope, baseline)

        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(first.stdout.strip(), second.stdout.strip())

    def test_invalid_scopes_are_rejected(self):
        invalid_scopes = [
            {"paths": ["/absolute"], "prefixes": []},
            {"paths": ["a/../b"], "prefixes": []},
            {"paths": [""], "prefixes": []},
            {"paths": ["a\\b"], "prefixes": []},
            {"paths": ["*.txt"], "prefixes": []},
            {"paths": ["a", "a"], "prefixes": []},
            {"paths": ["src/a"], "prefixes": ["src/"]},
            {"paths": [], "prefixes": ["src/", "src/nested/"]},
            {"paths": [], "prefixes": ["src"]},
            {"paths": [], "prefixes": [], "extra": True},
        ]
        for index, invalid in enumerate(invalid_scopes):
            with self.subTest(index=index, scope=invalid):
                scope = self._json_file("invalid-scope", invalid)
                result = self._run(
                    "baseline",
                    "--scope",
                    scope,
                    "--output",
                    "-",
                    "--contract",
                    "interface-contract.json",
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("scope", result.stderr.lower())

    def test_scope_json_rejects_nonfinite_values_and_duplicate_keys(self):
        invalid_values = [
            '{"paths":[NaN],"prefixes":[]}',
            '{"paths":[],"paths":["tracked.txt"],"prefixes":[]}',
        ]
        for index, value in enumerate(invalid_values):
            with self.subTest(index=index):
                scope = self._raw_json_file("strict-scope", value)
                result = self._run(
                    "baseline",
                    "--scope",
                    scope,
                    "--output",
                    "-",
                    "--contract",
                    "interface-contract.json",
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("invalid scope json", result.stderr.lower())

    def test_scope_json_rejects_unpaired_unicode_surrogate(self):
        scope = self._raw_json_file(
            "surrogate-scope",
            '{"paths":["\\ud800"],"prefixes":[]}',
        )
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "-",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid scope json", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())

    def test_contract_json_rejects_nonfinite_values_and_nested_duplicate_keys(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        invalid_values = [
            '{"version":"v1","status":"draft","value":Infinity}',
            '{"version":"v1","status":"draft","nested":{"key":1,"key":2}}',
        ]
        for index, value in enumerate(invalid_values):
            with self.subTest(index=index):
                (self.repo / "interface-contract.json").write_text(value, encoding="utf-8")
                result, _ = self._snapshot(scope, baseline)
                self.assertEqual(result.returncode, 2)
                self.assertIn("invalid contract json", result.stderr.lower())

    def test_contract_json_rejects_unpaired_unicode_surrogate(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        (self.repo / "interface-contract.json").write_text(
            '{"version":"\\ud800","status":"draft"}',
            encoding="utf-8",
        )
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "-",
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid contract json", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())

    def test_baseline_json_rejects_nonfinite_values_and_duplicate_keys(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        raw = baseline.read_text(encoding="utf-8")
        schema = "review-baseline-v2"
        invalid_values = [
            '{"poison":-Infinity,' + raw[1:],
            '{"schema":"' + schema + '",' + raw[1:],
        ]
        for index, value in enumerate(invalid_values):
            with self.subTest(index=index):
                invalid = self._raw_json_file("strict-baseline", value)
                result, _ = self._snapshot(scope, invalid)
                self.assertEqual(result.returncode, 2)
                self.assertIn("invalid baseline json", result.stderr.lower())

    def test_baseline_json_rejects_unpaired_unicode_surrogate(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        raw = baseline.read_text(encoding="utf-8")
        invalid = self._raw_json_file(
            "surrogate-baseline",
            raw.replace('"branch":"main"', '"branch":"\\ud800"', 1),
        )

        result, _ = self._snapshot(scope, invalid)

        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid baseline json", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())

    def test_snapshot_json_rejects_nonfinite_values_and_duplicate_keys(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        snapshot_result, snapshot = self._snapshot(scope, baseline)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        raw = snapshot.read_text(encoding="utf-8")
        snapshot_id = self._load_snapshot(snapshot)["id"]
        invalid_values = [
            '{"poison":NaN,' + raw[1:],
            '{"id":"' + snapshot_id + '",' + raw[1:],
        ]
        for index, value in enumerate(invalid_values):
            with self.subTest(index=index):
                invalid = self._raw_json_file("strict-snapshot", value)
                result = self._run(
                    "verify",
                    "--scope",
                    scope,
                    "--baseline",
                    baseline,
                    "--snapshot",
                    invalid,
                    "--contract",
                    "interface-contract.json",
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("invalid snapshot json", result.stderr.lower())

        invalid_shape = self._raw_json_file("strict-snapshot-shape", "[]\n")
        result = self._run(
            "verify",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--snapshot",
            invalid_shape,
            "--contract",
            "interface-contract.json",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid snapshot fields", result.stderr.lower())

    def test_snapshot_json_rejects_unpaired_unicode_surrogate(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        snapshot_result, snapshot = self._snapshot(scope, baseline)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        raw = snapshot.read_text(encoding="utf-8")
        invalid = self._raw_json_file(
            "surrogate-snapshot",
            raw.replace('"branch":"main"', '"branch":"\\ud800"', 1),
        )

        result = self._run(
            "verify",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--snapshot",
            invalid,
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid snapshot json", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())

    def test_invalid_contract_json_or_empty_fields_are_rejected(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        invalid_values = ["not-json", json.dumps({"version": "", "status": "draft"}), json.dumps({"version": "v1", "status": ""})]
        for index, value in enumerate(invalid_values):
            with self.subTest(index=index):
                (self.repo / "interface-contract.json").write_text(value, encoding="utf-8")
                result, _ = self._snapshot(scope, baseline)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("contract", result.stderr.lower())

    def test_baseline_rejects_invalid_contract(self):
        (self.repo / "interface-contract.json").write_text("not-json", encoding="utf-8")
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "-",
            "--contract",
            "interface-contract.json",
        )

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("contract", result.stderr.lower())

    def test_output_dash_returns_canonical_json_without_creating_files(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        before = self._worktree_entries()
        baseline_result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "-",
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(baseline_result.returncode, 0, baseline_result.stderr)
        self.assertFalse((self.repo / "-").exists())
        baseline_value = json.loads(baseline_result.stdout)
        self.assertTrue(baseline_result.stdout.endswith("\n"))
        baseline_path = self.root / "writer-persisted-baseline.json"
        self._persist_evidence(baseline_path, baseline_result.stdout)

        snapshot_result = self._run(
            "snapshot",
            "--scope",
            scope,
            "--baseline",
            baseline_path,
            "--output",
            "-",
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        self.assertFalse((self.repo / "-").exists())
        snapshot_value = json.loads(snapshot_result.stdout)
        self.assertEqual(self._worktree_entries(), before)
        self.assertEqual(baseline_value["schema"], "review-baseline-v2")
        self.assertRegex(snapshot_value["id"], r"^[0-9a-f]{64}$")

    def test_file_output_rejects_existing_regular_and_symlink_targets(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        regular = self.root / "existing-regular.json"
        regular.write_bytes(b"regular sentinel\n")
        symlink_target = self.root / "symlink-target.json"
        symlink_target.write_bytes(b"symlink sentinel\n")
        symlink = self.root / "existing-symlink.json"
        symlink.symlink_to(symlink_target)

        for name, output, protected, expected in (
            ("regular", regular, regular, b"regular sentinel\n"),
            ("symlink", symlink, symlink_target, b"symlink sentinel\n"),
        ):
            with self.subTest(kind=name):
                result = self._run(
                    "baseline",
                    "--scope",
                    scope,
                    "--output",
                    output,
                    "--contract",
                    "interface-contract.json",
                )
                self.assertEqual(result.returncode, 2)
                self.assertNotIn("traceback", result.stderr.lower())
                self.assertEqual(protected.read_bytes(), expected)

    def test_file_output_rejects_external_hardlink_to_worktree_inode(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        tracked = self.repo / "tracked.txt"
        output = self.root / "hardlinked-output.json"
        os.link(tracked, output)
        original = tracked.read_bytes()
        inode = tracked.stat().st_ino

        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            output,
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertNotIn("traceback", result.stderr.lower())
        self.assertEqual(tracked.stat().st_ino, inode)
        self.assertEqual(output.stat().st_ino, inode)
        self.assertEqual(tracked.read_bytes(), original)
        self.assertEqual(output.read_bytes(), original)

    def test_new_external_file_output_is_rejected_without_creation(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        output = self.root / "new-private-baseline.json"

        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            output,
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("only --output -", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())
        self.assertFalse(output.exists())

    def test_relative_file_output_is_rejected_without_creation(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "relative-evidence.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("only --output -", result.stderr.lower())
        self.assertNotIn("traceback", result.stderr.lower())
        self.assertFalse((self.repo / "relative-evidence.json").exists())

    def test_concurrent_file_output_attempts_are_all_rejected_without_creation(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        output = self.root / "raced-evidence.json"
        processes = [
            subprocess.Popen(
                [
                    sys.executable,
                    str(HELPER),
                    "baseline",
                    "--scope",
                    str(scope),
                    "--output",
                    str(output),
                ],
                cwd=self.repo,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            for _ in range(4)
        ]
        results = [process.communicate() + (process.returncode,) for process in processes]

        self.assertTrue(all(returncode == 2 for _, _, returncode in results))
        self.assertTrue(all("traceback" not in stderr.lower() for _, stderr, _ in results))
        self.assertFalse(output.exists())

    def test_baseline_and_snapshot_outputs_inside_worktree_are_rejected(self):
        scope = self._json_file("scope", {"paths": ["tracked.txt"], "prefixes": []})
        inside_baseline = self.repo / "evidence" / "baseline.json"
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            inside_baseline,
            "--contract",
            "interface-contract.json",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("only --output -", result.stderr.lower())
        self.assertFalse(inside_baseline.exists())

        alias_target = self.repo / "aliased-evidence"
        alias_target.mkdir()
        alias = self.root / "evidence-alias"
        os.symlink(alias_target, alias)
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            alias / "baseline.json",
            "--contract",
            "interface-contract.json",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("only --output -", result.stderr.lower())

        _, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        inside_snapshot = self.repo / "snapshot.json"
        result = self._run(
            "snapshot",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--output",
            inside_snapshot,
            "--contract",
            "interface-contract.json",
        )
        self.assertEqual(result.returncode, 2)
        self.assertIn("only --output -", result.stderr.lower())
        self.assertFalse(inside_snapshot.exists())

    def test_snapshot_rejects_baseline_input_inside_worktree(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        inside = self._write("evidence/baseline.json", baseline.read_text(encoding="utf-8"))

        result, _ = self._snapshot(scope, inside)

        self.assertEqual(result.returncode, 2)
        self.assertIn("baseline must be outside the git worktree", result.stderr.lower())

    def test_verify_rejects_worktree_evidence_and_symlink_aliases(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        snapshot_result, snapshot = self._snapshot(scope, baseline)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        inside_baseline = self._write("evidence/baseline.json", baseline.read_text(encoding="utf-8"))
        inside_snapshot = self._write("evidence/snapshot.json", snapshot.read_text(encoding="utf-8"))
        alias = self.root / "worktree-evidence-alias"
        os.symlink(self.repo / "evidence", alias)
        relative_baseline = "evidence/../evidence/baseline.json"
        cases = [
            (inside_baseline, snapshot),
            (baseline, inside_snapshot),
            (alias / "baseline.json", snapshot),
            (relative_baseline, snapshot),
        ]
        for index, (baseline_path, snapshot_path) in enumerate(cases):
            with self.subTest(index=index):
                result = self._run(
                    "verify",
                    "--scope",
                    scope,
                    "--baseline",
                    baseline_path,
                    "--snapshot",
                    snapshot_path,
                    "--contract",
                    "interface-contract.json",
                )
                self.assertEqual(result.returncode, 2)
                self.assertIn("outside the git worktree", result.stderr.lower())

    def test_verify_read_only_succeeds_for_matching_snapshot(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        snapshot_result, snapshot = self._snapshot(scope, baseline)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        before = self._worktree_entries()

        result = self._run(
            "verify",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--snapshot",
            snapshot,
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), self._load_snapshot(snapshot)["id"])
        self.assertEqual(self._worktree_entries(), before)
        self.assertEqual(list(self.repo.glob("-")), [])

    def test_verify_read_only_rejects_changed_content(self):
        scope, baseline = self._baseline({"paths": ["tracked.txt"], "prefixes": []})
        snapshot_result, snapshot = self._snapshot(scope, baseline)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)
        self._write("tracked.txt", "changed after snapshot\n")

        result = self._run(
            "verify",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--snapshot",
            snapshot,
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("snapshot verification failed", result.stderr.lower())

    def test_executable_mode_changes_fingerprint_and_snapshot_id(self):
        script = self._write("script.sh", "#!/bin/sh\nexit 0\n")
        scope, baseline = self._baseline({"paths": ["script.sh"], "prefixes": []})
        first, first_output = self._snapshot(scope, baseline)
        self.assertEqual(first.returncode, 0, first.stderr)

        script.chmod(0o755)
        second, second_output = self._snapshot(scope, baseline)

        self.assertEqual(second.returncode, 0, second.stderr)
        first_value = self._load_snapshot(first_output)
        second_value = self._load_snapshot(second_output)
        self.assertIn("mode", first_value["manifest"]["files"][0])
        self.assertIn("mode", second_value["manifest"]["files"][0])
        self.assertEqual(first_value["manifest"]["files"][0]["mode"], "100644")
        self.assertEqual(second_value["manifest"]["files"][0]["mode"], "100755")
        self.assertNotEqual(first_value["id"], second_value["id"])

    def test_exact_path_through_intermediate_symlink_is_rejected(self):
        external = self.root / "external"
        external.mkdir()
        (external / "secret.txt").write_text("outside\n", encoding="utf-8")
        os.symlink(str(external), self.repo / "linked")
        scope = self._json_file("scope", {"paths": ["linked/secret.txt"], "prefixes": []})
        result = self._run(
            "baseline",
            "--scope",
            scope,
            "--output",
            "-",
            "--contract",
            "interface-contract.json",
        )

        self.assertEqual(result.returncode, 2)
        self.assertIn("intermediate symlink", result.stderr.lower())

    def test_unmerged_index_stage_is_rejected(self):
        source = self._write("conflict-source.txt", "conflict\n")
        oid = self._git("hash-object", "-w", str(source)).stdout.strip()
        subprocess.run(
            ["git", "update-index", "--index-info"],
            cwd=self.repo,
            text=True,
            input=f"100644 {oid} 1\tconflict.txt\n",
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
        )
        scope = self._json_file("scope", {"paths": ["conflict.txt"], "prefixes": []})

        result = self._run("baseline", "--scope", scope, "--output", "-")

        self.assertEqual(result.returncode, 2)
        self.assertIn("unmerged index stage", result.stderr.lower())

    def test_gitlink_index_entry_is_rejected(self):
        head = self._git("rev-parse", "HEAD").stdout.strip()
        self._git("update-index", "--add", "--cacheinfo", f"160000,{head},vendor/sub")
        scope = self._json_file("scope", {"paths": ["vendor/sub"], "prefixes": []})

        result = self._run("baseline", "--scope", scope, "--output", "-")

        self.assertEqual(result.returncode, 2)
        self.assertIn("gitlink index entry", result.stderr.lower())

    def test_snapshot_and_verify_without_contract_use_null_manifest_contract(self):
        scope, baseline = self._baseline(
            {"paths": ["tracked.txt"], "prefixes": []}, contract=False
        )
        snapshot_result, snapshot = self._snapshot(scope, baseline, contract=False)
        self.assertEqual(snapshot_result.returncode, 0, snapshot_result.stderr)

        verify_result = self._run(
            "verify",
            "--scope",
            scope,
            "--baseline",
            baseline,
            "--snapshot",
            snapshot,
        )

        self.assertEqual(verify_result.returncode, 0, verify_result.stderr)
        value = self._load_snapshot(snapshot)
        self.assertIsNone(value["manifest"]["contract"])
        self.assertEqual(verify_result.stdout.strip(), value["id"])


if __name__ == "__main__":
    unittest.main()
