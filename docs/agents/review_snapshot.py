#!/usr/bin/env python3
"""Create reproducible review snapshots for dirty Git worktrees."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys


BASELINE_SCHEMA = "review-baseline-v2"
SNAPSHOT_SCHEMA = "review-snapshot-v2"
GLOB_META = frozenset("*?[]{}")


class SnapshotError(Exception):
    """Raised when a review snapshot cannot be safely produced."""


def canonical_json_bytes(value):
    try:
        return (
            json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(",", ":"))
            + "\n"
        ).encode("utf-8")
    except UnicodeEncodeError as error:
        raise SnapshotError("canonical JSON contains invalid UTF-8 text") from error


def sha256_bytes(value):
    return hashlib.sha256(value).hexdigest()


def _strict_json_bytes(raw, label):
    def reject_constant(value):
        raise ValueError(f"non-finite JSON constant is forbidden: {value}")

    def reject_duplicate_keys(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate JSON key is forbidden: {key!r}")
            result[key] = value
        return result

    def validate_utf8_strings(value):
        if isinstance(value, str):
            try:
                value.encode("utf-8")
            except UnicodeEncodeError as error:
                raise ValueError("unpaired Unicode surrogate is forbidden") from error
        elif isinstance(value, list):
            for item in value:
                validate_utf8_strings(item)
        elif isinstance(value, dict):
            for key, item in value.items():
                validate_utf8_strings(key)
                validate_utf8_strings(item)

    try:
        text = raw.decode("utf-8")
        value = json.loads(
            text,
            parse_constant=reject_constant,
            object_pairs_hook=reject_duplicate_keys,
        )
        validate_utf8_strings(value)
        return value
    except (UnicodeDecodeError, ValueError) as error:
        raise SnapshotError(f"invalid {label} JSON: {error}") from error


def _read_json(path, label):
    try:
        raw = Path(path).read_bytes()
    except OSError as error:
        raise SnapshotError(f"invalid {label} JSON: {error}") from error
    return _strict_json_bytes(raw, label), raw


def require_external_evidence_path(path, worktree, label):
    if path == "-":
        return
    root = os.path.realpath(worktree)
    resolved = os.path.realpath(os.path.abspath(path))
    try:
        inside = os.path.commonpath([root, resolved]) == root
    except ValueError:
        inside = False
    if inside:
        raise SnapshotError(f"{label} must be outside the Git worktree")


def _validate_scope_path(path, prefix):
    kind = "prefix" if prefix else "path"
    if not isinstance(path, str):
        raise SnapshotError(f"scope {kind} must be a UTF-8 string")
    try:
        path.encode("utf-8")
    except UnicodeEncodeError as error:
        raise SnapshotError(f"scope {kind} is not valid UTF-8") from error
    if not path or path.startswith("/") or "\\" in path or "\x00" in path:
        raise SnapshotError(f"invalid scope {kind}: {path!r}")
    if any(character in GLOB_META for character in path):
        raise SnapshotError(f"glob metadata is forbidden in scope {kind}: {path!r}")
    if prefix and not path.endswith("/"):
        raise SnapshotError(f"scope prefix must end with '/': {path!r}")
    if not prefix and path.endswith("/"):
        raise SnapshotError(f"exact scope path must not end with '/': {path!r}")
    body = path[:-1] if prefix else path
    parts = body.split("/")
    if not body or any(part in ("", ".", "..") for part in parts):
        raise SnapshotError(f"ambiguous scope {kind}: {path!r}")


def load_scope(path):
    value, _ = _read_json(path, "scope")
    if not isinstance(value, dict) or set(value) != {"paths", "prefixes"}:
        raise SnapshotError("scope JSON must contain only paths and prefixes")
    paths = value["paths"]
    prefixes = value["prefixes"]
    if not isinstance(paths, list) or not isinstance(prefixes, list):
        raise SnapshotError("scope paths and prefixes must be arrays")
    for item in paths:
        _validate_scope_path(item, prefix=False)
    for item in prefixes:
        _validate_scope_path(item, prefix=True)
    if len(paths) != len(set(paths)) or len(prefixes) != len(set(prefixes)):
        raise SnapshotError("scope contains duplicate paths or prefixes")
    sorted_paths = sorted(paths, key=lambda item: item.encode("utf-8"))
    sorted_prefixes = sorted(prefixes, key=lambda item: item.encode("utf-8"))
    for index, first in enumerate(sorted_prefixes):
        for second in sorted_prefixes[index + 1 :]:
            if second.startswith(first):
                raise SnapshotError(f"scope prefixes overlap: {first!r} and {second!r}")
    for exact in sorted_paths:
        for prefix in sorted_prefixes:
            if exact.startswith(prefix):
                raise SnapshotError(f"scope path overlaps prefix: {exact!r} and {prefix!r}")
    return {"paths": sorted_paths, "prefixes": sorted_prefixes}


def validate_contract_path(path):
    if path is None:
        return None
    try:
        _validate_scope_path(path, prefix=False)
    except SnapshotError as error:
        raise SnapshotError(f"invalid contract path: {error}") from error
    return path


def _git(worktree, *args, check=True):
    result = subprocess.run(
        ["git", *args],
        cwd=worktree,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    if check and result.returncode != 0:
        message = result.stderr.decode("utf-8", "replace").strip()
        raise SnapshotError(f"git {' '.join(args)} failed: {message}")
    return result


def _decode_git_text(raw, label):
    try:
        return raw.rstrip(b"\n").decode("utf-8")
    except UnicodeDecodeError as error:
        raise SnapshotError(f"git {label} is not valid UTF-8") from error


def repository_identity(start):
    worktree_raw = _git(start, "rev-parse", "--path-format=absolute", "--show-toplevel").stdout
    common_raw = _git(start, "rev-parse", "--path-format=absolute", "--git-common-dir").stdout
    worktree = os.path.realpath(_decode_git_text(worktree_raw, "worktree"))
    common_dir = Path(os.path.realpath(_decode_git_text(common_raw, "common dir")))
    repository = os.path.realpath(str(common_dir.parent))
    branch_result = _git(start, "symbolic-ref", "--quiet", "--short", "HEAD", check=False)
    branch = (
        _decode_git_text(branch_result.stdout, "branch")
        if branch_result.returncode == 0
        else "DETACHED"
    )
    head = _decode_git_text(_git(start, "rev-parse", "HEAD").stdout, "HEAD")
    return {
        "repository": repository,
        "worktree": worktree,
        "branch": branch,
        "head": head,
    }


def _decode_git_path(raw):
    try:
        path = raw.decode("utf-8")
    except UnicodeDecodeError as error:
        raise SnapshotError("git candidate path is not valid UTF-8") from error
    if not path or path.startswith("/") or "\x00" in path:
        raise SnapshotError(f"invalid git candidate path: {path!r}")
    return path


def _name_status_candidates(raw):
    tokens = raw.split(b"\0")
    if tokens and tokens[-1] == b"":
        tokens.pop()
    candidates = set()
    index = 0
    while index < len(tokens):
        try:
            status_value = tokens[index].decode("ascii")
        except UnicodeDecodeError as error:
            raise SnapshotError("invalid git name-status record") from error
        index += 1
        status_code = status_value[:1]
        path_count = 2 if status_code in ("R", "C") else 1
        if not status_code or index + path_count > len(tokens):
            raise SnapshotError("truncated git name-status record")
        for raw_path in tokens[index : index + path_count]:
            candidates.add(_decode_git_path(raw_path))
        index += path_count
    return candidates


def git_candidates(worktree, base_revision, scope):
    worktree_raw = _git(
        worktree,
        "diff",
        "--name-status",
        "-z",
        base_revision,
        "--find-renames",
        "--find-copies-harder",
    ).stdout
    cached_raw = _git(
        worktree,
        "diff",
        "--cached",
        "--name-status",
        "-z",
        base_revision,
        "--find-renames",
        "--find-copies-harder",
    ).stdout
    candidates = _name_status_candidates(worktree_raw)
    candidates.update(_name_status_candidates(cached_raw))
    visible_untracked = _git(
        worktree, "ls-files", "--others", "--exclude-standard", "-z"
    ).stdout
    for raw_path in visible_untracked.split(b"\0"):
        if raw_path:
            candidates.add(_decode_git_path(raw_path))
    all_untracked = _git(worktree, "ls-files", "--others", "-z").stdout
    for raw_path in all_untracked.split(b"\0"):
        if raw_path:
            path = _decode_git_path(raw_path)
            if is_in_scope(path, scope):
                candidates.add(path)
    return candidates


def git_index(worktree):
    raw = _git(worktree, "ls-files", "--stage", "-z").stdout
    result = {}
    for record in raw.split(b"\0"):
        if not record:
            continue
        header, separator, raw_path = record.partition(b"\t")
        if not separator:
            raise SnapshotError("invalid git index record")
        fields = header.split(b" ")
        if len(fields) != 3:
            raise SnapshotError("invalid git index metadata")
        try:
            mode = fields[0].decode("ascii")
            oid = fields[1].decode("ascii")
            stage = int(fields[2].decode("ascii"))
        except (UnicodeDecodeError, ValueError) as error:
            raise SnapshotError("invalid git index metadata") from error
        if (
            not mode
            or not mode.isdigit()
            or not oid
            or any(character not in "0123456789abcdef" for character in oid)
            or stage not in (0, 1, 2, 3)
        ):
            raise SnapshotError("invalid git index metadata")
        path = _decode_git_path(raw_path)
        if stage != 0:
            raise SnapshotError(f"unmerged index stage is forbidden: {path}")
        if mode == "160000":
            raise SnapshotError(f"gitlink index entry is unsupported: {path}")
        result.setdefault(path, []).append({"mode": mode, "oid": oid, "stage": stage})
    for entries in result.values():
        entries.sort(key=lambda item: (item["stage"], item["mode"], item["oid"]))
    return result


def is_in_scope(path, scope):
    return path in scope["paths"] or any(path.startswith(prefix) for prefix in scope["prefixes"])


def confined_path(worktree, relative_path):
    root = Path(os.path.realpath(worktree))
    parts = relative_path.split("/")
    if not parts or any(part in ("", ".", "..") for part in parts):
        raise SnapshotError(f"invalid fingerprint path: {relative_path!r}")
    current = root
    for component in parts[:-1]:
        current = current / component
        try:
            metadata = current.lstat()
        except FileNotFoundError:
            break
        if stat.S_ISLNK(metadata.st_mode):
            raise SnapshotError(f"intermediate symlink is forbidden: {relative_path}")
        if not stat.S_ISDIR(metadata.st_mode):
            raise SnapshotError(f"intermediate component is not a directory: {relative_path}")
    path = root.joinpath(*parts)
    parent_real = os.path.realpath(str(path.parent))
    try:
        if os.path.commonpath([str(root), parent_real]) != str(root):
            raise SnapshotError(f"fingerprint path escapes worktree: {relative_path}")
    except ValueError as error:
        raise SnapshotError(f"fingerprint path escapes worktree: {relative_path}") from error
    return path


def file_fingerprint(worktree, relative_path):
    path = confined_path(worktree, relative_path)
    try:
        metadata = path.lstat()
    except (FileNotFoundError, NotADirectoryError):
        return {"path": relative_path, "type": "absent", "mode": None, "sha256": None}
    if stat.S_ISREG(metadata.st_mode):
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
        executable = metadata.st_mode & (stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
        mode = "100755" if executable else "100644"
        return {
            "path": relative_path,
            "type": "regular",
            "mode": mode,
            "sha256": digest.hexdigest(),
        }
    if stat.S_ISLNK(metadata.st_mode):
        try:
            target = os.readlink(str(path)).encode("utf-8")
        except UnicodeEncodeError as error:
            raise SnapshotError(f"symlink target is not valid UTF-8: {relative_path}") from error
        return {
            "path": relative_path,
            "type": "symlink",
            "mode": "120000",
            "sha256": sha256_bytes(target),
        }
    raise SnapshotError(f"unsupported file type: {relative_path}")


def sorted_fingerprints(worktree, paths, index):
    fingerprints = []
    for path in sorted(paths, key=lambda item: item.encode("utf-8")):
        fingerprint = file_fingerprint(worktree, path)
        entries = index.get(path)
        fingerprint["index"] = (
            {"type": "entries", "entries": entries}
            if entries
            else {"type": "absent", "entries": []}
        )
        fingerprints.append(fingerprint)
    return fingerprints


def contract_record(worktree, relative):
    relative = validate_contract_path(relative)
    if relative is None:
        return None
    path = confined_path(worktree, relative)
    try:
        if not stat.S_ISREG(path.lstat().st_mode):
            raise SnapshotError("contract must be a regular file")
        raw = path.read_bytes()
        value = _strict_json_bytes(raw, "contract")
    except SnapshotError:
        raise
    except OSError as error:
        raise SnapshotError(f"invalid contract JSON: {error}") from error
    if not isinstance(value, dict):
        raise SnapshotError("contract JSON must be an object")
    version = value.get("version")
    status_value = value.get("status")
    if not isinstance(version, str) or not version or not isinstance(status_value, str) or not status_value:
        raise SnapshotError("contract version and status must be non-empty strings")
    return {
        "path": relative,
        "sha256": sha256_bytes(raw),
        "version": version,
        "status": status_value,
    }


def build_baseline(scope_path, contract_path):
    scope = load_scope(scope_path)
    identity = repository_identity(os.getcwd())
    contract_path = validate_contract_path(contract_path)
    contract_record(identity["worktree"], contract_path)
    index = git_index(identity["worktree"])
    sorted_fingerprints(identity["worktree"], scope["paths"], index)
    candidates = git_candidates(identity["worktree"], identity["head"], scope)
    tracked = set(index)
    outside = {
        path for path in candidates | tracked if not is_in_scope(path, scope)
    }
    baseline = {
        "schema": BASELINE_SCHEMA,
        **identity,
        "contract_path": contract_path,
        "scope": scope,
        "out_of_scope": sorted_fingerprints(identity["worktree"], outside, index),
    }
    return baseline


def write_stdout(value, output_path):
    if output_path != "-":
        raise SnapshotError("only --output - is supported")
    sys.stdout.buffer.write(canonical_json_bytes(value))


def create_baseline(scope_path, output_path, contract_path):
    if output_path != "-":
        raise SnapshotError("only --output - is supported")
    result = build_baseline(scope_path, contract_path)
    write_stdout(result, output_path)


def load_baseline(path):
    value, raw = _read_json(path, "baseline")
    expected_keys = {
        "schema", "repository", "worktree", "branch", "head", "contract_path",
        "scope", "out_of_scope"
    }
    if not isinstance(value, dict) or set(value) != expected_keys:
        raise SnapshotError("invalid baseline fields")
    if value["schema"] != BASELINE_SCHEMA:
        raise SnapshotError("unsupported baseline schema")
    if canonical_json_bytes(value) != raw:
        raise SnapshotError("baseline JSON is not canonical")
    return value, sha256_bytes(raw)


def build_snapshot(scope_path, baseline_path, contract_path):
    scope = load_scope(scope_path)
    contract_path = validate_contract_path(contract_path)
    identity = repository_identity(os.getcwd())
    require_external_evidence_path(baseline_path, identity["worktree"], "baseline")
    baseline, baseline_sha256 = load_baseline(baseline_path)
    for field in ("repository", "worktree", "branch"):
        if baseline[field] != identity[field]:
            raise SnapshotError(f"baseline {field} does not match current worktree")
    if baseline["scope"] != scope:
        raise SnapshotError("baseline scope does not match requested scope")
    if baseline["contract_path"] != contract_path:
        raise SnapshotError("baseline contract path does not match requested contract")

    contract = contract_record(identity["worktree"], contract_path)
    candidates = git_candidates(identity["worktree"], baseline["head"], scope)
    index = git_index(identity["worktree"])
    tracked = set(index)
    outside = {
        path for path in candidates | tracked if not is_in_scope(path, scope)
    }
    current_outside = sorted_fingerprints(identity["worktree"], outside, index)
    if current_outside != baseline["out_of_scope"]:
        raise SnapshotError("out-of-scope candidate fingerprint changed")

    inside_candidates = {path for path in candidates if is_in_scope(path, scope)}
    tracked_in_scope = {
        path
        for path in tracked
        if is_in_scope(path, scope)
    }
    manifest_paths = set(scope["paths"]) | inside_candidates | tracked_in_scope
    manifest = {
        "schema": SNAPSHOT_SCHEMA,
        **identity,
        "contract": contract,
        "scope": scope,
        "files": sorted_fingerprints(identity["worktree"], manifest_paths, index),
    }
    snapshot_id = sha256_bytes(canonical_json_bytes(manifest))
    result = {
        "id": snapshot_id,
        "baseline_sha256": baseline_sha256,
        "manifest": manifest,
    }
    return result


def create_snapshot(scope_path, baseline_path, output_path, contract_path):
    if output_path != "-":
        raise SnapshotError("only --output - is supported")
    result = build_snapshot(scope_path, baseline_path, contract_path)
    write_stdout(result, output_path)


def verify_snapshot(scope_path, baseline_path, snapshot_path, contract_path):
    identity = repository_identity(os.getcwd())
    require_external_evidence_path(baseline_path, identity["worktree"], "baseline")
    require_external_evidence_path(snapshot_path, identity["worktree"], "snapshot")
    supplied, raw = _read_json(snapshot_path, "snapshot")
    if not isinstance(supplied, dict):
        raise SnapshotError("invalid snapshot fields")
    if canonical_json_bytes(supplied) != raw:
        raise SnapshotError("snapshot verification failed: supplied JSON is not canonical")
    current = build_snapshot(scope_path, baseline_path, contract_path)
    if raw != canonical_json_bytes(current) or supplied.get("id") != current["id"]:
        raise SnapshotError("snapshot verification failed: current snapshot differs")
    print(current["id"])


def parse_args(argv):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    baseline = subparsers.add_parser("baseline")
    baseline.add_argument("--scope", required=True)
    baseline.add_argument("--output", required=True)
    baseline.add_argument("--contract")
    snapshot = subparsers.add_parser("snapshot")
    snapshot.add_argument("--scope", required=True)
    snapshot.add_argument("--baseline", required=True)
    snapshot.add_argument("--output", required=True)
    snapshot.add_argument("--contract")
    verify = subparsers.add_parser("verify")
    verify.add_argument("--scope", required=True)
    verify.add_argument("--baseline", required=True)
    verify.add_argument("--snapshot", required=True)
    verify.add_argument("--contract")
    return parser.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    try:
        if args.command == "baseline":
            create_baseline(args.scope, args.output, args.contract)
        elif args.command == "snapshot":
            create_snapshot(args.scope, args.baseline, args.output, args.contract)
        else:
            verify_snapshot(args.scope, args.baseline, args.snapshot, args.contract)
    except SnapshotError as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
