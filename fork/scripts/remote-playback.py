#!/usr/bin/env python3
"""Reproduce pinned remote-playback patches without touching production."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit


class RunnerError(Exception):
    pass


def confined_path(base, relative, label):
    if not isinstance(relative, str) or not relative or "\\" in relative:
        raise RunnerError(f"invalid {label} path")
    path = Path(relative)
    if path.is_absolute() or ".." in path.parts:
        raise RunnerError(f"{label} must be a confined relative path")
    current = base
    for part in path.parts:
        current = current / part
        if current.is_symlink():
            raise RunnerError(f"{label} path must not use symlinks")
    resolved = (base / path).resolve()
    if not resolved.is_relative_to(base.resolve()):
        raise RunnerError(f"{label} symlink escapes its directory")
    return resolved


def validate_repository(repository):
    url = urlsplit(repository)
    if (url.scheme != "https" or url.netloc != "github.com" or
            url.username or url.password or url.query or url.fragment or
            not re.fullmatch(r"/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git", url.path)):
        raise RunnerError("repository must be a credential-free GitHub HTTPS URL")


def runtime_source_keys(projects, key):
    ordered = []
    visiting = set()
    visited = set()

    def visit(current):
        if current in visiting:
            raise RunnerError("runtime_sources dependency cycle")
        if current in visited:
            return
        visiting.add(current)
        dependencies = projects[current].get("runtime_sources", [])
        if (not isinstance(dependencies, list) or
                any(not isinstance(dependency, str) or dependency not in projects or dependency == current
                    for dependency in dependencies) or len(set(dependencies)) != len(dependencies)):
            raise RunnerError(f"{current}: runtime_sources must name distinct other manifest projects")
        for dependency in dependencies:
            if dependency not in ordered:
                ordered.append(dependency)
            visit(dependency)
        visiting.remove(current)
        visited.add(current)

    visit(key)
    return ordered


def load_manifest(root):
    manifest_path = confined_path(root, "fork/patches/remote-playback/manifest.json", "manifest")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    workspace = confined_path(root, manifest["workspace"], "workspace")
    if workspace != (root / "tmp/remote-playback").resolve():
        raise RunnerError("workspace must be tmp/remote-playback")
    if manifest.get("schema_version") != 1:
        raise RunnerError("unsupported manifest schema_version")
    if manifest.get("sandbox") != {"network": "none", "read_only": True, "pull_policy": "never"}:
        raise RunnerError("sandbox must be network none, read-only, pull never")
    projects = manifest.get("projects")
    if not isinstance(projects, dict) or not projects:
        raise RunnerError("manifest needs projects")
    patch_dir = manifest_path.parent
    for key, project in projects.items():
        if not re.fullmatch(r"[a-z][a-z0-9_-]*", key):
            raise RunnerError("invalid project key")
        validate_repository(project["repository"])
        if not re.fullmatch(r"[0-9a-f]{40}", project["revision"]):
            raise RunnerError(f"{key}: revision must be an exact commit")
        if not re.fullmatch(r"[a-z0-9][a-z0-9./_-]*@sha256:[0-9a-f]{64}", project["runtime_image"]):
            raise RunnerError(f"{key}: runtime_image must be digest-pinned")
        checkout = confined_path(workspace, f"src/{key}", "checkout")
        confined_path(checkout, project["source_path"], "source_path")
        command = project["test_command"]
        if command is not None and (not isinstance(command, list) or len(command) != 9 or
                command[:7] != ["python3", "-B", "-m", "unittest", "discover", "-s", "tests"] or
                command[7] != "-p" or not isinstance(command[8], str) or
                not re.fullmatch(r"test_[A-Za-z0-9_*?-]+\.py", command[8])):
            raise RunnerError(f"{key}: test_command must be Python unittest discovery in tests")
        if not isinstance(project["patches"], list):
            raise RunnerError(f"{key}: patches must be a list")
        seen = set()
        for patch in project["patches"]:
            path = confined_path(patch_dir, patch["file"], "patch")
            if path.suffix != ".patch" or path in seen:
                raise RunnerError(f"{key}: patches must name distinct .patch files")
            seen.add(path)
            if not re.fullmatch(r"[0-9a-f]{64}", patch["sha256"]):
                raise RunnerError(f"{key}: invalid patch sha256")
            if hashlib.sha256(path.read_bytes()).hexdigest() != patch["sha256"]:
                raise RunnerError(f"{key}: patch checksum mismatch: {patch['file']}")
    tested_sources = set()
    for key, project in projects.items():
        dependencies = runtime_source_keys(projects, key)
        if project["test_command"] is not None:
            tested_sources.update(dependencies)
    if any(project["test_command"] is None and key not in tested_sources
           for key, project in projects.items()):
        raise RunnerError("source-only projects must be reachable via runtime_sources from a tested project")
    return manifest


def execute(run, argv, cwd):
    inherited = {
        "PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "XDG_RUNTIME_DIR",
        "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH",
    }
    env = {key: value for key, value in os.environ.items() if key in inherited}
    env.update(GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL="/dev/null", GIT_TERMINAL_PROMPT="0",
               GIT_CEILING_DIRECTORIES=str(cwd), GIT_OPTIONAL_LOCKS="0", GIT_NO_LAZY_FETCH="1")
    result = run(argv, cwd=str(cwd), env=env, text=True,
                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if result.returncode:
        raise RunnerError(f"{argv[0]} failed ({result.returncode}): {result.stderr.strip()}")
    return result


def git(run, checkout, *args):
    if args[0] == "clone":
        metadata = confined_path(checkout, ".git", "clone parent metadata")
        if metadata.exists():
            raise RunnerError("clone parent must not be a Git repository")
        context = []
    else:
        validate_local_config(checkout)
        context = [f"--git-dir={checkout / '.git'}", f"--work-tree={checkout}"]
    command = ["git", *context, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", *args]
    return execute(run, command, checkout).stdout


def provenance_path(checkout):
    return confined_path(checkout.parent, checkout.name + ".json", "provenance")


def config_entries(path):
    if not path.is_file() or not stat.S_ISREG(path.stat().st_mode):
        raise RunnerError("Git local config must be a regular file")
    section = None
    subsection = None
    # Only the non-executable clone/sparse subset is accepted, never includes or continuations.
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith(("#", ";")):
            continue
        if line.startswith("["):
            header = re.fullmatch(r'\[([A-Za-z][A-Za-z0-9-]*)(?:[ \t]+"([A-Za-z0-9_./-]+)")?\][ \t]*(?:[#;].*)?', line)
            if not header:
                raise RunnerError("unsupported Git local config section syntax")
            section, subsection = header.groups()
            section = section.lower()
            continue
        option = re.fullmatch(r"([A-Za-z][A-Za-z0-9-]*)[ \t]*(?:=[ \t]*(.*))?", line)
        if section is None or not option:
            raise RunnerError("unsupported Git local config option syntax")
        key, raw = option.groups()
        if raw is None:
            value = "true"
        else:
            parsed = re.fullmatch(r'"([^"\\\x00-\x1f]*)"[ \t]*(?:[#;].*)?', raw)
            if parsed is not None:
                value = parsed.group(1)
            else:
                parsed = re.fullmatch(r'([^"\\\x00-\x1f#;]*)(?:[#;].*)?', raw)
                if parsed is None:
                    raise RunnerError("unsupported Git local config value syntax")
                value = parsed.group(1).strip()
        yield section, subsection, key.lower(), value


def validate_local_config(checkout):
    metadata = confined_path(checkout, ".git", "Git metadata")
    for name in ("commondir", "gitdir", "objects/info/alternates", "objects/info/http-alternates"):
        pointer = confined_path(metadata, name, "Git metadata pointer")
        if pointer.exists():
            raise RunnerError("Git metadata redirection/alternates are not permitted")
    validate_mount_tree(metadata, allow_internal_symlinks=False)
    booleans = {"true", "false", "yes", "no", "on", "off", "0", "1"}
    false_values = {"false", "no", "off", "0"}
    for filename in ("config", "config.worktree"):
        path = confined_path(metadata, filename, "Git local config")
        if filename == "config.worktree" and not path.exists():
            continue
        for section, subsection, key, value in config_entries(path):
            normalized = value.lower()
            allowed = False
            if section == "core" and subsection is None:
                if key == "repositoryformatversion":
                    allowed = value in {"0", "1"}
                elif key in {"filemode", "logallrefupdates", "sparsecheckout", "sparsecheckoutcone", "ignorecase", "symlinks"}:
                    allowed = normalized in booleans
                elif key in {"bare", "fsmonitor"}:
                    allowed = normalized in false_values
                elif key == "worktree":
                    allowed = value == str(checkout)
                elif key == "hookspath":
                    allowed = value == "/dev/null"
            elif section == "remote" and subsection == "origin":
                if key == "url":
                    validate_repository(value)
                    allowed = True
                elif key == "fetch":
                    allowed = ".." not in value and bool(re.fullmatch(
                        r"\+?refs/(?:heads|tags)/[A-Za-z0-9_./*-]+:refs/(?:tags|remotes/origin)/[A-Za-z0-9_./*-]+", value))
                elif key == "promisor":
                    allowed = normalized in booleans
                elif key == "partialclonefilter":
                    allowed = value == "blob:none"
            elif section == "extensions" and subsection is None:
                if key == "worktreeconfig":
                    allowed = normalized in booleans
                elif key == "partialclone":
                    allowed = value == "origin"
                elif key == "objectformat":
                    allowed = value == "sha1"
            elif section == "index" and subsection is None:
                allowed = key == "sparse" and normalized in booleans
            elif section == "user" and subsection is None:
                allowed = key in {"name", "email"}
            elif section == "branch" and subsection is not None and ".." not in subsection:
                if key == "remote":
                    allowed = value == "origin"
                elif key == "merge":
                    allowed = ".." not in value and bool(re.fullmatch(r"refs/heads/[A-Za-z0-9_./-]+", value))
            if not allowed:
                raise RunnerError("Git local config contains unsupported or unsafe setting")


def read_provenance(checkout, project):
    marker = provenance_path(checkout)
    metadata = confined_path(checkout, ".git", "Git metadata")
    if not checkout.is_dir() or not metadata.is_dir() or not marker.is_file():
        raise RunnerError("existing checkout is not runner-generated; refusing to adopt or overwrite")
    validate_local_config(checkout)
    provenance = json.loads(marker.read_text(encoding="utf-8"))
    if (not isinstance(provenance, dict) or
            type(provenance.get("schema_version")) is not int or provenance["schema_version"] != 1 or
            provenance.get("project") != checkout.name or
            any(provenance.get(field) != project[field] for field in ("repository", "revision"))):
        raise RunnerError("checkout provenance schema/project/pins do not match manifest")
    return provenance


def inspect_checkout(run, checkout, project):
    read_provenance(checkout, project)
    identity = git(run, checkout, "rev-parse", "--show-toplevel", "--absolute-git-dir").splitlines()
    expected = [checkout, checkout / ".git"]
    if (len(identity) != 2 or any(not Path(path).is_absolute() or Path(path).resolve() != target
                                  for path, target in zip(identity, expected))):
        raise RunnerError("Git effective worktree/metadata escaped the owned checkout")
    if git(run, checkout, "remote", "get-url", "origin").strip() != project["repository"]:
        raise RunnerError("checkout origin does not match manifest")
    head = git(run, checkout, "rev-parse", "HEAD").strip()
    dirty = bool(git(run, checkout, "status", "--porcelain=v1", "--untracked-files=all").strip())
    return {"state": "dirty" if dirty else ("locked" if head == project["revision"] else "wrong-revision"),
            "head": head, "dirty": dirty}


def fetch_checkout(run, checkout, project):
    if checkout.exists():
        state = inspect_checkout(run, checkout, project)
        if state["state"] != "locked":
            raise RunnerError("existing checkout is dirty or at wrong revision; no reset performed")
        return state
    marker = provenance_path(checkout)
    if marker.exists():
        raise RunnerError("orphaned provenance exists; inspect incomplete checkout manually")
    checkout.parent.mkdir(parents=True, exist_ok=True)
    git(run, checkout.parent, "clone", "--no-checkout", "--no-hardlinks", "--template=", "--", project["repository"], str(checkout))
    git(run, checkout, "checkout", "--detach", project["revision"])
    if git(run, checkout, "rev-parse", "HEAD").strip() != project["revision"]:
        raise RunnerError("clone did not produce pinned revision")
    marker.write_text(json.dumps({
        "schema_version": 1, "project": checkout.name,
        "repository": project["repository"], "revision": project["revision"],
    }) + "\n", encoding="utf-8")
    return inspect_checkout(run, checkout, project)


def apply_patches(run, checkout, project, patch_dir):
    state = inspect_checkout(run, checkout, project)
    if state["state"] != "locked":
        raise RunnerError("apply requires clean pinned checkout; no changes discarded")
    paths = [str(confined_path(patch_dir, patch["file"], "patch")) for patch in project["patches"]]
    if paths:
        git(run, checkout, "apply", "--check", "--index", *paths)
        git(run, checkout, "apply", "--index", *paths)
        provenance = read_provenance(checkout, project)
        provenance.update(applied_tree=git(run, checkout, "write-tree").strip(),
                          applied_patches=project["patches"])
        provenance_path(checkout).write_text(json.dumps(provenance) + "\n", encoding="utf-8")
    return {"applied": len(paths)}


def validate_mount_tree(checkout, *, allow_internal_symlinks=True):
    def fail_walk(error):
        raise error

    for directory, dirs, files in os.walk(checkout, followlinks=False, onerror=fail_walk):
        for name in dirs + files:
            path = Path(directory) / name
            mode = path.lstat().st_mode
            if stat.S_ISLNK(mode):
                if not allow_internal_symlinks or not path.resolve().is_relative_to(checkout):
                    raise RunnerError("checkout contains unsafe symlink; refusing access")
            elif not stat.S_ISREG(mode) and not stat.S_ISDIR(mode):
                raise RunnerError("checkout contains socket/device/FIFO; refusing access")


def validate_test_source(run, checkout, project, *, runtime_source=False):
    if runtime_source and ":" in project["source_path"]:
        raise RunnerError("runtime source_path cannot contain PYTHONPATH separators")
    state = inspect_checkout(run, checkout, project)
    if state["head"] != project["revision"]:
        raise RunnerError("test requires pinned base revision")
    if state["dirty"]:
        provenance = read_provenance(checkout, project)
        tree = git(run, checkout, "write-tree").strip()
        if (not tree or tree != provenance.get("applied_tree") or
                provenance.get("applied_patches") != project["patches"]):
            raise RunnerError("test refuses dirty changes not produced by canonical apply")
        git(run, checkout, "diff", "--no-ext-diff", "--no-textconv", "--quiet")
    if state["dirty"] or runtime_source:
        exclusions = [] if runtime_source else ["--exclude-standard"]
        if git(run, checkout, "ls-files", "--others", *exclusions).strip():
            raise RunnerError("test refuses untracked files; export/stage them first")
    source = confined_path(checkout, project["source_path"], "source_path")
    if not source.is_dir():
        raise RunnerError("source_path directory missing")
    validate_mount_tree(checkout)
    if "," in str(checkout):
        raise RunnerError("checkout path cannot contain Docker mount separators")
    return source


def test_checkout(run, checkout, project, projects=None):
    if project["test_command"] is None:
        raise RunnerError("source-only project; test dependent project instead")
    validate_test_source(run, checkout, project)
    tests = confined_path(checkout, "tests", "tests")
    if not tests.is_dir():
        raise RunnerError("tests directory missing")
    matches = list(tests.glob(project["test_command"][8]))
    if not matches:
        raise RunnerError("no matching tests; refusing zero-test success")
    for path in matches:
        confined_path(checkout, str(path.relative_to(checkout)), "test file")
    pythonpaths = ["/workspace" if project["source_path"] == "." else "/workspace/" + project["source_path"]]
    mounts = ["--mount", f"type=bind,src={checkout},dst=/workspace,readonly"]
    for key in runtime_source_keys(projects if projects is not None else {checkout.name: project}, checkout.name):
        dependency = confined_path(checkout.parent, key, "runtime source checkout")
        source = validate_test_source(run, dependency, projects[key], runtime_source=True)
        destination = Path("/runtime-sources") / key
        mounts.extend(["--mount", f"type=bind,src={dependency},dst={destination},readonly"])
        pythonpaths.append(str(destination / source.relative_to(dependency)))
    command = ["docker", "run", "--rm", "--pull=never", "--network", "none", "--read-only",
               "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--tmpfs", "/tmp",
               *mounts, "--workdir", "/workspace",
               "--env", f"PYTHONPATH={':'.join(pythonpaths)}", "--entrypoint", project["test_command"][0],
               project["runtime_image"], *project["test_command"][1:]]
    result = execute(run, command, checkout)
    output = result.stdout + result.stderr
    runs = list(re.finditer(r"^Ran (\d+) tests? in ", output, re.MULTILINE))
    if not runs or int(runs[-1].group(1)) == 0:
        raise RunnerError("runtime did not report nonzero unittest execution")
    count = int(runs[-1].group(1))
    summary = output[runs[-1].end():]
    skipped = re.findall(r"^OK \([^\n)]*\bskipped=(\d+)[^\n)]*\)$", summary, re.MULTILINE)
    if skipped and int(skipped[-1]) >= count:
        raise RunnerError("runtime did not report non-skipped unittest execution")
    return {"tests": count, "output": output}


def atomic_write(path, content):
    with tempfile.NamedTemporaryFile(dir=path.parent, prefix=".remote-playback-", delete=False) as stream:
        temporary = Path(stream.name)
        try:
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        except BaseException:
            temporary.unlink()
            raise
    try:
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def export_patch(run, checkout, manifest, key, patch_dir, patch_name, working_tree, source_commit):
    if bool(working_tree) == bool(source_commit):
        raise RunnerError("export requires exactly one of --working-tree or --source-commit")
    if source_commit and not re.fullmatch(r"[0-9a-f]{40}", source_commit):
        raise RunnerError("source-commit must be an exact commit")
    if not patch_name:
        raise RunnerError("export requires --patch relative .patch path")
    target = confined_path(patch_dir, patch_name, "patch")
    if target.suffix != ".patch" or not target.parent.is_dir():
        raise RunnerError("export needs .patch in existing patch directory")
    owners = {name for name, project in manifest["projects"].items()
              if any(confined_path(patch_dir, patch["file"], "patch") == target for patch in project["patches"])}
    if owners - {key} or (target.exists() and key not in owners):
        raise RunnerError("export will not overwrite an unowned or another project's patch")
    project = manifest["projects"][key]
    state = inspect_checkout(run, checkout, project)
    if git(run, checkout, "ls-files", "--others", "--exclude-standard").strip():
        raise RunnerError("untracked files cannot be exported: stage or commit them first")
    endpoint = source_commit or state["head"]
    if git(run, checkout, "cat-file", "-t", endpoint).strip() != "commit":
        raise RunnerError("export endpoint is not a commit")
    git(run, checkout, "merge-base", "--is-ancestor", project["revision"], endpoint)
    revisions = [project["revision"]] + ([source_commit] if source_commit else [])
    diff = git(run, checkout, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", *revisions)
    if not diff:
        raise RunnerError("no tracked changes to export")
    content = diff.encode("utf-8")
    checksum = hashlib.sha256(content).hexdigest()
    previous = target.read_bytes() if target.exists() else None
    project["patches"] = [{"file": patch_name, "sha256": checksum}]
    manifest_path = patch_dir / "manifest.json"
    manifest_path.read_bytes()
    atomic_write(target, content)
    try:
        atomic_write(manifest_path, (json.dumps(manifest, indent=2) + "\n").encode("utf-8"))
    except BaseException:
        if previous is None:
            target.unlink()
        else:
            atomic_write(target, previous)
        raise
    return {"patch": patch_name, "sha256": checksum, "source": source_commit or "working-tree"}


def main(argv=None, *, repo_root=None, run=subprocess.run, stdout=None, stderr=None):
    stdout = stdout or sys.stdout
    stderr = stderr or sys.stderr
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["validate", "status", "fetch", "apply", "test", "export"])
    parser.add_argument("project", nargs="?")
    parser.add_argument("--working-tree", action="store_true", help="export tracked/staged working tree changes")
    parser.add_argument("--source-commit", help="export exact source commit relative to pinned base")
    parser.add_argument("--patch", help="canonical patch path relative to fork/patches/remote-playback")
    args = parser.parse_args(argv)
    root = Path(repo_root or Path(__file__).resolve().parents[2]).resolve()
    try:
        if args.command != "export" and (args.working_tree or args.source_commit or args.patch):
            raise RunnerError("these options are for only export")
        manifest = load_manifest(root)
        projects = manifest["projects"]
        if args.project is not None and args.project not in projects:
            raise RunnerError("unknown project")
        if args.command == "validate":
            result = {"projects": sorted(projects)}
        elif args.command == "status":
            result = {}
            for key in ([args.project] if args.project else sorted(projects)):
                checkout = confined_path(root, f"{manifest['workspace']}/src/{key}", "checkout")
                result[key] = inspect_checkout(run, checkout, projects[key]) if checkout.exists() else {"state": "missing"}
        else:
            if args.project is None:
                raise RunnerError("this command requires a project")
            project = projects[args.project]
            checkout = confined_path(root, f"{manifest['workspace']}/src/{args.project}", "checkout")
            if args.command == "fetch":
                result = fetch_checkout(run, checkout, project)
            elif args.command == "apply":
                result = apply_patches(run, checkout, project, root / "fork/patches/remote-playback")
            elif args.command == "test":
                result = test_checkout(run, checkout, project, projects)
            elif args.command == "export":
                result = export_patch(
                    run, checkout, manifest, args.project, root / "fork/patches/remote-playback",
                    args.patch, args.working_tree, args.source_commit)
            else:
                raise RunnerError("command not implemented")
        print(json.dumps(result), file=stdout)
        return 0
    except (RunnerError, OSError, ValueError, KeyError, TypeError) as error:
        print(f"error: {error}", file=stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
