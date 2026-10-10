import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import socket
import tempfile
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "remote-playback.py"
spec = importlib.util.spec_from_file_location("remote_playback", SCRIPT)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


BASE_CONFIG = """[core]
repositoryformatversion = 1
filemode = true
bare = false
logallrefupdates = true
[remote "origin"]
url = https://github.com/example/bridge.git
fetch = +refs/tags/v1:refs/tags/v1
promisor = true
partialclonefilter = blob:none
"""


def fixture_repository(checkout, repository="https://github.com/example/bridge.git"):
    metadata = checkout / ".git"
    metadata.mkdir(parents=True)
    config = BASE_CONFIG.replace("https://github.com/example/bridge.git", repository)
    (metadata / "config").write_text(config, encoding="utf-8")


class FakeCommands:
    def __init__(self):
        self.calls = []
        self.responses = {}
        self.checkouts = {}
        self.head = "1" * 40
        self.origin = "https://github.com/example/bridge.git"
        self.dirty = ""
        self.tree = "3" * 40
        self.diff = "abc"
        self.untracked = ""
        self.fail_check = False
        self.test_output = "Ran 1 test in 0.001s\n\nOK\n"
        self.tamper_config = ""
        self.effective_worktree = None

    def __call__(self, argv, **kwargs):
        self.calls.append((argv, kwargs))
        state = self.checkouts.get(Path(kwargs["cwd"]).name, {})
        if argv[0] == "git" and self.tamper_config:
            config = Path(kwargs["cwd"]) / ".git/config"
            config.write_text(BASE_CONFIG + self.tamper_config, encoding="utf-8")
            self.tamper_config = ""
        if self.fail_check and "apply" in argv and "--check" in argv:
            return subprocess.CompletedProcess(argv, 1, "", "fixture patch check failure")
        result = self.responses.get(tuple(argv))
        if result is None:
            output = ""
            if argv[0] == "git":
                if "clone" in argv:
                    fixture_repository(Path(argv[-1]))
                elif "rev-parse" in argv:
                    if "--show-toplevel" in argv:
                        checkout = Path(kwargs["cwd"])
                        effective = state.get("effective_worktree", self.effective_worktree) or checkout
                        output = str(effective) + "\n" + str(checkout / ".git") + "\n"
                    else:
                        output = state.get("head", self.head) + "\n"
                elif "remote" in argv:
                    output = state.get("origin", self.origin) + "\n"
                elif "status" in argv:
                    output = state.get("dirty", self.dirty)
                elif "write-tree" in argv:
                    output = state.get("tree", self.tree) + "\n"
                elif "diff" in argv and "--quiet" in argv and state.get("unstaged"):
                    return subprocess.CompletedProcess(argv, 1, "", "fixture unstaged changes")
                elif "diff" in argv and "--quiet" not in argv:
                    output = self.diff
                elif "ls-files" in argv:
                    output = state.get("untracked", self.untracked)
                    if "--exclude-standard" not in argv:
                        output += state.get("ignored", "")
                elif "cat-file" in argv:
                    output = "commit\n"
            if argv[0] == "docker":
                output = ""
                result = (0, output, self.test_output)
            else:
                result = (0, output, "")
        return subprocess.CompletedProcess(argv, *result)


class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.patch_dir = self.root / "fork/patches/remote-playback"
        self.patch_dir.mkdir(parents=True)
        self.manifest_path = self.patch_dir / "manifest.json"
        self.manifest = {
            "schema_version": 1,
            "workspace": "tmp/remote-playback",
            "projects": {
                "bridge": {
                    "repository": "https://github.com/example/bridge.git",
                    "revision": "1" * 40,
                    "source_path": "src",
                    "runtime_image": "ghcr.io/example/bridge@sha256:" + "2" * 64,
                    "test_command": ["python3", "-B", "-m", "unittest", "discover", "-s", "tests", "-p", "test_output_preparation*.py"],
                    "patches": [],
                }
            },
            "sandbox": {"network": "none", "read_only": True, "pull_policy": "never"},
        }
        self.commands = FakeCommands()
        self.out = io.StringIO()
        self.err = io.StringIO()
        self.save_manifest()

    def save_manifest(self):
        self.manifest_path.write_text(json.dumps(self.manifest), encoding="utf-8")

    def invoke(self, *args):
        return runner.main(list(args), repo_root=self.root, run=self.commands,
                           stdout=self.out, stderr=self.err)

    def generated_checkout(self, key="bridge"):
        project = self.manifest["projects"][key]
        checkout = self.root / "tmp/remote-playback/src" / key
        fixture_repository(checkout, project["repository"])
        checkout.with_suffix(".json").write_text(json.dumps({
            "schema_version": 1,
            "project": key,
            "repository": project["repository"],
            "revision": project["revision"],
        }), encoding="utf-8")
        if key != "bridge":
            self.commands.checkouts[key] = {"head": project["revision"], "origin": project["repository"]}
        return checkout

    def add_runtime_source(self, key="sendspin", *, generated=True):
        self.manifest["projects"][key] = dict(
            self.manifest["projects"]["bridge"],
            repository=f"https://github.com/example/{key}.git", revision="4" * 40,
        )
        self.manifest["projects"][key].pop("runtime_sources", None)
        self.manifest["projects"]["bridge"]["runtime_sources"] = [key]
        self.save_manifest()
        if not generated:
            return self.root / "tmp/remote-playback/src" / key
        checkout = self.generated_checkout(key)
        (checkout / "src").mkdir()
        return checkout

    def add_patch(self, key="bridge"):
        (self.patch_dir / f"{key}.patch").write_bytes(b"abc")
        self.manifest["projects"][key]["patches"] = [{
            "file": f"{key}.patch",
            "sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
        }]
        self.save_manifest()

    def fixture_sources(self, checkout):
        (checkout / "src").mkdir()
        (checkout / "tests").mkdir()
        (checkout / "tests/test_output_preparation_example.py").write_text("# fixture", encoding="utf-8")

    def unittest_output(self, *, include_execution=False):
        class FixtureCases(unittest.TestCase):
            @unittest.skip("synthetic dependency unavailable")
            def test_skipped(self):
                pass

            def test_executed(self):
                pass

        cases = [FixtureCases("test_skipped")]
        if include_execution:
            cases.append(FixtureCases("test_executed"))
        stream = io.StringIO()
        unittest.TextTestRunner(stream=stream).run(unittest.TestSuite(cases))
        return stream.getvalue()

    def test_validate_reports_locked_projects_without_external_commands(self):
        self.assertEqual(self.invoke("validate"), 0)
        self.assertEqual(json.loads(self.out.getvalue())["projects"], ["bridge"])
        self.assertEqual(len(self.commands.calls), 0)

    def test_validate_rejects_workspace_escape_without_commands(self):
        self.manifest["workspace"] = "tmp/../data"
        self.save_manifest()
        self.assertEqual(self.invoke("validate"), 1)
        self.assertIn("workspace", self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)

    def test_validate_rejects_unpinned_or_unsafe_execution_inputs(self):
        cases = [
            ("repository", "https://secret@github.com/example/bridge.git"),
            ("repository", "file:///data/production"),
            ("repository", "https://github.com/example/bridge.git?token=secret"),
            ("revision", "main"),
            ("runtime_image", "bridge:latest"),
            ("source_path", "../data"),
            ("test_command", ["sh", "-c", "touch /workspace/changed"]),
            ("patches", [{"file": "../../secret", "sha256": "3" * 64}]),
        ]
        original = dict(self.manifest["projects"]["bridge"])
        for field, value in cases:
            with self.subTest(field=field, value=value):
                self.manifest["projects"]["bridge"] = dict(original, **{field: value})
                self.save_manifest()
                self.assertEqual(self.invoke("validate"), 1)
        self.manifest["projects"]["bridge"] = original
        self.manifest["sandbox"]["network"] = "host"
        self.save_manifest()
        self.assertEqual(self.invoke("validate"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_validate_rejects_invalid_runtime_source_references_without_commands(self):
        self.add_runtime_source()
        cases = ["sendspin", None, {}, [1], ["sendspin", "sendspin"], ["bridge"], ["missing"], ["../sendspin"]]
        for dependencies in cases:
            with self.subTest(runtime_sources=dependencies):
                self.manifest["projects"]["bridge"]["runtime_sources"] = dependencies
                self.save_manifest()
                self.assertEqual(self.invoke("validate"), 1)
                self.assertIn("runtime_sources", self.err.getvalue())
                self.assertEqual(len(self.commands.calls), 0)

    def test_validate_rejects_runtime_source_cycles_without_commands(self):
        self.add_runtime_source()
        self.manifest["projects"]["sendspin"]["runtime_sources"] = ["bridge"]
        self.save_manifest()
        self.assertEqual(self.invoke("validate"), 1)
        self.assertIn("runtime_sources", self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)

    def test_validate_refuses_source_only_projects_without_reachability_from_tested_root(self):
        self.add_runtime_source(generated=False)
        command = self.manifest["projects"]["bridge"]["test_command"]
        self.manifest["projects"]["sendspin"]["test_command"] = None
        for dependencies, root_command in (([], command), (["sendspin"], None)):
            with self.subTest(runtime_sources=dependencies, root_command=root_command):
                self.manifest["projects"]["bridge"].update(runtime_sources=dependencies, test_command=root_command)
                self.save_manifest()
                self.assertEqual(self.invoke("validate"), 1)
                self.assertIn("source-only", self.err.getvalue())
                self.assertEqual(len(self.commands.calls), 0)

    def test_validate_never_uses_symlinked_workspace_or_patch_directories(self):
        (self.root / "data").mkdir()
        (self.root / "tmp").symlink_to(self.root / "data", target_is_directory=True)
        self.assertEqual(self.invoke("validate"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_status_reports_missing_checkout_without_creating_it(self):
        self.assertEqual(self.invoke("status", "bridge"), 0)
        self.assertEqual(json.loads(self.out.getvalue())["bridge"]["state"], "missing")
        self.assertFalse((self.root / "tmp").exists())
        self.assertEqual(len(self.commands.calls), 0)

    def test_fetch_clones_only_generated_workspace_and_checks_out_exact_commit(self):
        self.assertEqual(self.invoke("fetch", "bridge"), 0, self.err.getvalue())
        checkout = self.root / "tmp/remote-playback/src/bridge"
        clones = [argv for argv, _ in self.commands.calls if "clone" in argv]
        self.assertEqual(len(clones), 1)
        self.assertEqual(clones[0][-2:], ["https://github.com/example/bridge.git", str(checkout)])
        self.assertTrue(any(argv[-3:] == ["checkout", "--detach", "1" * 40]
                            for argv, _ in self.commands.calls))
        self.assertEqual(json.loads(self.out.getvalue())["state"], "locked")
        self.assertFalse((self.root / "data").exists())

    def test_apply_checks_then_stages_verified_patches_without_destructive_git_options(self):
        self.generated_checkout()
        self.add_patch()
        self.assertEqual(self.invoke("apply", "bridge"), 0, self.err.getvalue())
        applies = [argv for argv, _ in self.commands.calls if "apply" in argv]
        self.assertEqual(len(applies), 2)
        self.assertIn("--check", applies[0])
        self.assertNotIn("--check", applies[1])
        self.assertTrue(all("--index" in argv for argv in applies))
        self.assertTrue(all("--reject" not in argv and "--unsafe-paths" not in argv for argv in applies))
        self.assertEqual(json.loads(self.out.getvalue())["applied"], 1)

    def test_test_runs_only_digest_pinned_readonly_offline_container(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        docker = [argv for argv, _ in self.commands.calls if argv[0] == "docker"]
        self.assertEqual(len(docker), 1)
        command = docker[0]
        for flag in ["--rm", "--pull=never", "--read-only"]:
            self.assertIn(flag, command)
        for option, value in [("--network", "none"), ("--cap-drop", "ALL"),
                              ("--security-opt", "no-new-privileges"),
                              ("--tmpfs", "/tmp"), ("--entrypoint", "python3"),
                              ("--workdir", "/workspace")]:
            self.assertEqual(command[command.index(option) + 1], value)
        self.assertEqual(command[command.index("--mount") + 1],
                         f"type=bind,src={checkout},dst=/workspace,readonly")
        self.assertIn("PYTHONPATH=/workspace/src", command)
        self.assertEqual(command.count("--mount"), 1)
        self.assertEqual(command[-9:], ["ghcr.io/example/bridge@sha256:" + "2" * 64,
                         "-B", "-m", "unittest", "discover", "-s", "tests", "-p", "test_output_preparation*.py"])
        for forbidden in ["--privileged", "--device", "--publish", "-p", "--volume"]:
            self.assertNotIn(forbidden, command[:command.index("ghcr.io/example/bridge@sha256:" + "2" * 64)])
        self.assertEqual(json.loads(self.out.getvalue())["tests"], 1)

    def test_test_mounts_owned_runtime_sources_readonly_without_dependency_tests(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        docker = [argv for argv, _ in self.commands.calls if argv[0] == "docker"]
        self.assertEqual(len(docker), 1)
        command = docker[0]
        mounts = [command[index + 1] for index, value in enumerate(command) if value == "--mount"]
        self.assertEqual(mounts, [
            f"type=bind,src={checkout},dst=/workspace,readonly",
            f"type=bind,src={dependency},dst=/runtime-sources/sendspin,readonly",
        ])
        self.assertIn("PYTHONPATH=/workspace/src:/runtime-sources/sendspin/src", command)
        self.assertFalse((dependency / "tests").exists())
        self.assertFalse(any("clone" in argv or "apply" in argv or "checkout" in argv
                             for argv, _ in self.commands.calls))

    def test_test_mounts_source_only_runtime_source_with_null_command_without_dependency_tests(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        self.manifest["projects"]["sendspin"].update(test_command=None, source_path=".")
        self.save_manifest()
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        command = next(argv for argv, _ in self.commands.calls if argv[0] == "docker")
        self.assertEqual(command.count("--mount"), 2)
        self.assertIn(f"type=bind,src={dependency},dst=/runtime-sources/sendspin,readonly", command)
        self.assertIn("PYTHONPATH=/workspace/src:/runtime-sources/sendspin", command)
        self.assertFalse((dependency / "tests").exists())
        self.assertEqual(json.loads(self.out.getvalue())["tests"], 1)

    def test_test_refuses_standalone_source_only_project_before_docker(self):
        self.add_runtime_source()
        self.manifest["projects"]["sendspin"]["test_command"] = None
        self.save_manifest()
        self.assertEqual(self.invoke("test", "sendspin"), 1)
        self.assertIn("source-only project", self.err.getvalue())
        self.assertIn("test dependent project", self.err.getvalue())
        self.assertEqual(self.out.getvalue(), "")
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_source_only_projects_preserve_status_fetch_apply_and_export_commands(self):
        self.add_runtime_source()
        self.manifest["projects"]["sendspin"]["test_command"] = None
        self.add_patch("sendspin")
        for command in ("status", "fetch", "apply"):
            with self.subTest(command=command):
                self.assertEqual(self.invoke(command, "sendspin"), 0, self.err.getvalue())
        self.commands.checkouts["sendspin"]["dirty"] = "M  src/worker.py\n"
        self.assertEqual(self.invoke("export", "sendspin", "--working-tree", "--patch", "sendspin.patch"), 0,
                         self.err.getvalue())
        self.assertIsNone(json.loads(self.manifest_path.read_text())["projects"]["sendspin"]["test_command"])
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_mounts_transitive_runtime_sources_once_with_confined_pythonpaths(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_runtime_source()
        codec = self.add_runtime_source("codec")
        self.manifest["projects"]["sendspin"]["runtime_sources"] = ["codec"]
        self.manifest["projects"]["codec"]["source_path"] = "."
        for dependencies in (["sendspin"], ["sendspin", "codec"]):
            with self.subTest(runtime_sources=dependencies):
                self.manifest["projects"]["bridge"]["runtime_sources"] = dependencies
                self.save_manifest()
                self.commands.calls.clear()
                self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
                command = next(argv for argv, _ in self.commands.calls if argv[0] == "docker")
                self.assertEqual(command.count("--mount"), 3)
                self.assertIn(f"type=bind,src={codec},dst=/runtime-sources/codec,readonly", command)
                self.assertIn("PYTHONPATH=/workspace/src:/runtime-sources/sendspin/src:/runtime-sources/codec", command)

    def test_test_mounts_source_only_transitive_dependencies_once_from_tested_root(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        codec = self.add_runtime_source("codec")
        self.manifest["projects"]["bridge"]["runtime_sources"] = ["sendspin", "codec"]
        self.manifest["projects"]["sendspin"].update(
            test_command=None, source_path=".", runtime_sources=["codec"],
        )
        self.manifest["projects"]["codec"].update(test_command=None, source_path=".")
        self.save_manifest()
        self.assertEqual(self.invoke("validate"), 0, self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        command = next(argv for argv, _ in self.commands.calls if argv[0] == "docker")
        self.assertEqual(command.count("--mount"), 3)
        self.assertIn(f"type=bind,src={dependency},dst=/runtime-sources/sendspin,readonly", command)
        self.assertIn(f"type=bind,src={codec},dst=/runtime-sources/codec,readonly", command)
        self.assertIn("PYTHONPATH=/workspace/src:/runtime-sources/sendspin:/runtime-sources/codec", command)
        self.assertFalse((dependency / "tests").exists())
        self.assertFalse((codec / "tests").exists())

    def test_source_only_runtime_sources_still_reject_untrusted_checkout_before_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_runtime_source()
        self.manifest["projects"]["sendspin"]["test_command"] = None
        self.save_manifest()
        valid = self.commands.checkouts["sendspin"]
        cases = [{"head": "5" * 40}, {"dirty": "M  src/worker.py\n"}, {"ignored": "src/ignored.py\n"}]
        for changes in cases:
            with self.subTest(changes=changes):
                self.commands.checkouts["sendspin"] = dict(valid, **changes)
                self.commands.calls.clear()
                self.assertEqual(self.invoke("test", "bridge"), 1)
                self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_refuses_missing_runtime_source_without_fetch_or_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source(generated=False)
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertIn("not runner-generated", self.err.getvalue())
        self.assertFalse(dependency.exists())
        self.assertFalse(any(argv[0] == "docker" or "clone" in argv or "checkout" in argv or "apply" in argv
                             for argv, _ in self.commands.calls))

    def test_test_refuses_runtime_sources_with_missing_or_mismatched_provenance(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        marker = dependency.with_suffix(".json")
        valid = json.loads(marker.read_text())
        cases = [None, dict(valid, project="bridge"), dict(valid, schema_version=2),
                 dict(valid, revision="5" * 40), dict(valid, repository="https://github.com/example/other.git")]
        for provenance in cases:
            with self.subTest(provenance=provenance):
                if provenance is None:
                    marker.unlink()
                else:
                    marker.write_text(json.dumps(provenance), encoding="utf-8")
                self.commands.calls.clear()
                self.assertEqual(self.invoke("test", "bridge"), 1)
                self.assertFalse(any(argv[0] == "docker" or options["cwd"] == str(dependency)
                                     for argv, options in self.commands.calls))

    def test_test_refuses_runtime_source_identity_origin_or_revision_mismatches(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_runtime_source()
        valid = self.commands.checkouts["sendspin"]
        cases = [("effective_worktree", self.root / "outside"),
                 ("origin", "https://github.com/example/other.git"), ("head", "5" * 40)]
        for field, value in cases:
            with self.subTest(field=field):
                self.commands.checkouts["sendspin"] = dict(valid, **{field: value})
                self.commands.calls.clear()
                self.assertEqual(self.invoke("test", "bridge"), 1)
                self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_refuses_runtime_source_unsafe_config_before_dependency_git_or_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        (dependency / ".git/config.worktree").write_text('[include]\npath = ../outside\n', encoding="utf-8")
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertFalse(any(argv[0] == "docker" or options["cwd"] == str(dependency)
                             for argv, options in self.commands.calls))

    def test_test_accepts_runner_applied_runtime_source_staged_tree(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        self.add_patch("sendspin")
        self.assertEqual(self.invoke("apply", "sendspin"), 0, self.err.getvalue())
        self.commands.checkouts["sendspin"]["dirty"] = "M  src/worker.py\n"
        self.commands.calls.clear()
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        command = next(argv for argv, _ in self.commands.calls if argv[0] == "docker")
        self.assertIn(f"type=bind,src={dependency},dst=/runtime-sources/sendspin,readonly", command)
        self.assertFalse(any("apply" in argv or "clone" in argv or "checkout" in argv
                             for argv, _ in self.commands.calls))

    def test_test_refuses_changed_canonical_runtime_source_without_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_runtime_source()
        self.add_patch("sendspin")
        self.assertEqual(self.invoke("apply", "sendspin"), 0, self.err.getvalue())
        valid = dict(self.commands.checkouts["sendspin"], dirty="M  src/worker.py\n")
        cases = [("unknown index", {"tree": "5" * 40}),
                 ("unstaged", {"unstaged": True}), ("untracked", {"untracked": "src/extra.py\n"})]
        for name, changes in cases:
            with self.subTest(case=name):
                self.commands.checkouts["sendspin"] = dict(valid, **changes)
                self.commands.calls.clear()
                self.assertEqual(self.invoke("test", "bridge"), 1)
                self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_refuses_escaping_runtime_source_path_before_any_command(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_runtime_source()
        self.manifest["projects"]["sendspin"]["source_path"] = "../bridge/src"
        self.save_manifest()
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_test_refuses_runtime_source_pythonpath_separator_ambiguity_before_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        source_path = "src:/unowned"
        (dependency / source_path).mkdir(parents=True)
        self.manifest["projects"]["sendspin"]["source_path"] = source_path
        self.save_manifest()
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertIn("PYTHONPATH", self.err.getvalue())
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_refuses_symlinked_runtime_source_checkout_before_any_command(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        outside = self.root / "outside"
        dependency.rename(outside)
        dependency.symlink_to(outside, target_is_directory=True)
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_test_validates_entire_runtime_source_mount_tree_before_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        hidden = dependency / ".hidden"
        hidden.mkdir()
        unsafe = hidden / "control"
        for kind in ("escaping symlink", "FIFO"):
            with self.subTest(kind=kind):
                if kind == "FIFO":
                    os.mkfifo(unsafe)
                else:
                    unsafe.symlink_to(self.root, target_is_directory=True)
                try:
                    self.commands.calls.clear()
                    self.assertEqual(self.invoke("test", "bridge"), 1)
                    self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))
                finally:
                    unsafe.unlink()

    def test_test_refuses_missing_or_symlinked_runtime_source_directory(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        source = dependency / "src"
        source.rmdir()
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertIn("source_path directory missing", self.err.getvalue())
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))
        internal = dependency / "internal"
        internal.mkdir()
        source.symlink_to(internal, target_is_directory=True)
        self.commands.calls.clear()
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_test_refuses_ignored_untracked_runtime_source_files_before_docker(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        dependency = self.add_runtime_source()
        (dependency / "src/ignored.py").write_text("# untrusted ignored fixture", encoding="utf-8")
        self.commands.checkouts["sendspin"]["ignored"] = "src/ignored.py\n"
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_accepts_only_runner_applied_staged_tree(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.add_patch()
        self.assertEqual(self.invoke("apply", "bridge"), 0)
        self.commands.dirty = "A  tests/test_output_preparation_example.py\n"
        self.out.seek(0)
        self.out.truncate()
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        self.assertEqual(json.loads(self.out.getvalue())["tests"], 1)

    def test_export_records_git_diff_from_locked_base_and_updates_checksum(self):
        self.generated_checkout()
        self.commands.dirty = "M  tracked.py\n"
        self.assertEqual(self.invoke("export", "bridge", "--working-tree", "--patch", "bridge.patch"), 0,
                         self.err.getvalue())
        self.assertEqual((self.patch_dir / "bridge.patch").read_bytes(), b"abc")
        exported = json.loads(self.manifest_path.read_text())["projects"]["bridge"]["patches"]
        self.assertEqual(exported, [{"file": "bridge.patch", "sha256":
                         "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}])
        diffs = [argv for argv, _ in self.commands.calls if "diff" in argv]
        self.assertEqual(len(diffs), 1)
        self.assertIn("--binary", diffs[0])
        self.assertEqual(diffs[0][-1], "1" * 40)
        self.assertEqual(json.loads(self.out.getvalue())["source"], "working-tree")

    def test_export_can_select_exact_source_commit_without_checking_it_out(self):
        self.generated_checkout()
        self.commands.head = "4" * 40
        self.assertEqual(self.invoke("export", "bridge", "--source-commit", "5" * 40,
                                     "--patch", "bridge.patch"), 0, self.err.getvalue())
        diffs = [argv for argv, _ in self.commands.calls if "diff" in argv]
        self.assertEqual(diffs[0][-2:], ["1" * 40, "5" * 40])
        self.assertFalse(any("checkout" in argv or "reset" in argv for argv, _ in self.commands.calls))
        self.assertEqual(json.loads(self.out.getvalue())["source"], "5" * 40)

    def test_fetch_refuses_symlinked_git_metadata_before_any_git_command(self):
        checkout = self.generated_checkout()
        (checkout / ".git/config").unlink()
        (checkout / ".git").rmdir()
        external = self.root / "data/production-git"
        external.mkdir(parents=True)
        (checkout / ".git").symlink_to(external, target_is_directory=True)
        self.assertEqual(self.invoke("fetch", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_test_refuses_host_sockets_hidden_inside_mounted_checkout(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        sock = socket.socket(socket.AF_UNIX)
        self.addCleanup(sock.close)
        sock.bind(str(checkout / ".git/docker.sock"))
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_fetch_preserves_unowned_dirty_and_wrong_revision_checkouts(self):
        checkout = self.root / "tmp/remote-playback/src/bridge"
        checkout.mkdir(parents=True)
        self.assertEqual(self.invoke("fetch", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)
        checkout.rmdir()
        self.generated_checkout()
        for dirty, head in [(" M keep.py\n", "1" * 40), ("", "4" * 40)]:
            with self.subTest(dirty=dirty, head=head):
                self.commands.dirty = dirty
                self.commands.head = head
                self.commands.calls.clear()
                self.assertEqual(self.invoke("fetch", "bridge"), 1)
                self.assertFalse(any("checkout" in argv or "reset" in argv or "clone" in argv
                                     for argv, _ in self.commands.calls))

    def test_apply_rejects_checksum_mismatch_before_git(self):
        self.generated_checkout()
        self.add_patch()
        (self.patch_dir / "bridge.patch").write_bytes(b"modified")
        self.assertEqual(self.invoke("apply", "bridge"), 1)
        self.assertIn("checksum mismatch", self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)

    def test_apply_stops_after_external_check_failure(self):
        self.generated_checkout()
        self.add_patch()
        self.commands.fail_check = True
        self.assertEqual(self.invoke("apply", "bridge"), 1)
        self.assertEqual(len([argv for argv, _ in self.commands.calls if "apply" in argv]), 1)
        self.assertIn("fixture patch check failure", self.err.getvalue())

    def test_test_refuses_unrecognized_dirty_index(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.commands.dirty = "M  changed.py\n"
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertFalse(any(argv[0] == "docker" for argv, _ in self.commands.calls))

    def test_test_refuses_empty_suite_even_when_process_exits_zero(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.commands.test_output = "Ran 0 tests in 0.001s\n\nOK\n"
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertIn("nonzero unittest", self.err.getvalue())

    def test_test_refuses_suite_when_every_test_is_skipped(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.commands.test_output = self.unittest_output()
        self.assertEqual(self.invoke("test", "bridge"), 1)
        self.assertIn("non-skipped unittest", self.err.getvalue())

    def test_test_does_not_confuse_inner_skipped_suite_with_executed_outer_suite(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        self.commands.test_output = self.unittest_output() + self.commands.test_output
        self.assertEqual(self.invoke("test", "bridge"), 0, self.err.getvalue())
        self.assertEqual(json.loads(self.out.getvalue())["tests"], 1)

    def test_export_never_silently_drops_untracked_files(self):
        self.generated_checkout()
        self.commands.untracked = "tests/new_test.py\n"
        before = self.manifest_path.read_bytes()
        self.assertEqual(self.invoke("export", "bridge", "--working-tree", "--patch", "bridge.patch"), 1)
        self.assertIn("stage or commit", self.err.getvalue())
        self.assertEqual(self.manifest_path.read_bytes(), before)
        self.assertFalse((self.patch_dir / "bridge.patch").exists())
        self.assertFalse(any("diff" in argv for argv, _ in self.commands.calls))

    def test_status_reports_malformed_provenance_as_error_without_git(self):
        checkout = self.generated_checkout()
        checkout.with_suffix(".json").write_text("[]", encoding="utf-8")
        self.assertEqual(self.invoke("status", "bridge"), 1)
        self.assertIn("provenance", self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)

    def test_validate_rejects_export_options_instead_of_silently_ignoring_them(self):
        self.assertEqual(self.invoke("validate", "--working-tree"), 1)
        self.assertIn("only export", self.err.getvalue())
        self.assertEqual(len(self.commands.calls), 0)

    def test_fetch_writes_versioned_project_ownership_provenance(self):
        self.assertEqual(self.invoke("fetch", "bridge"), 0)
        marker = self.root / "tmp/remote-playback/src/bridge.json"
        self.assertEqual(json.loads(marker.read_text()), {
            "schema_version": 1,
            "project": "bridge",
            "repository": "https://github.com/example/bridge.git",
            "revision": "1" * 40,
        })

    def test_status_refuses_wrong_project_or_unversioned_ownership_marker(self):
        checkout = self.generated_checkout()
        marker = checkout.with_suffix(".json")
        valid = json.loads(marker.read_text())
        cases = [dict(valid, project="ma"), dict(valid, schema_version=2),
                 {key: value for key, value in valid.items() if key != "schema_version"}]
        for provenance in cases:
            with self.subTest(provenance=provenance):
                marker.write_text(json.dumps(provenance), encoding="utf-8")
                self.assertEqual(self.invoke("status", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_git_and_docker_ports_do_not_inherit_unrelated_host_credentials(self):
        checkout = self.generated_checkout()
        self.fixture_sources(checkout)
        os.environ["RUNNER_TEST_PRIVATE_TOKEN"] = "synthetic-fixture"
        self.addCleanup(os.environ.pop, "RUNNER_TEST_PRIVATE_TOKEN", None)
        self.assertEqual(self.invoke("test", "bridge"), 0)
        self.assertTrue(any(argv[0] == "git" for argv, _ in self.commands.calls))
        self.assertTrue(any(argv[0] == "docker" for argv, _ in self.commands.calls))
        for _, options in self.commands.calls:
            self.assertTrue("RUNNER_TEST_PRIVATE_TOKEN" not in options["env"])
            self.assertEqual(options["env"]["GIT_CONFIG_GLOBAL"], "/dev/null")

    def test_apply_refuses_symlinked_local_config_before_any_external_command(self):
        checkout = self.generated_checkout()
        self.add_patch()
        outside = self.root / "outside"
        outside.mkdir()
        config = outside / "config"
        config.write_text(f"[core]\nworktree = {outside}\n[filter \"fixture\"]\nclean = fixture-command\n",
                          encoding="utf-8")
        (checkout / ".git/config").unlink()
        (checkout / ".git/config").symlink_to(config)
        self.assertEqual(self.invoke("apply", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)

    def test_apply_fails_closed_for_unsafe_local_config_and_worktree_config(self):
        checkout = self.generated_checkout()
        self.add_patch()
        outside = self.root / "outside"
        cases = {
            "include": f"[include]\npath = {outside}/config\n",
            "conditional-include": f'[includeIf "gitdir:{checkout}/"]\npath = {outside}/config\n',
            "filter-clean": '[filter "fixture"]\nclean = fixture-command\n',
            "filter-process": '[filter "fixture"]\nprocess = fixture-command\n',
            "worktree-redirect": f"[core]\nworktree = {outside}\n",
            "bare": "[core]\nbare = true\n",
            "fsmonitor": "[core]\nfsmonitor = fixture-command\n",
            "hooks": f"[core]\nhooksPath = {outside}/hooks\n",
            "unsafe-extension": "[extensions]\nrelativeWorktrees = true\n",
            "remote-helper": '[remote "origin"]\nurl = ext::fixture-command\n',
            "url-rewrite": '[url "ext::fixture-command"]\ninsteadOf = https://github.com/\n',
        }
        for filename in ("config", "config.worktree"):
            for name, addition in cases.items():
                with self.subTest(filename=filename, case=name):
                    (checkout / ".git/config").write_text(BASE_CONFIG, encoding="utf-8")
                    (checkout / ".git/config.worktree").unlink(missing_ok=True)
                    (checkout / ".git" / filename).write_text(BASE_CONFIG + addition, encoding="utf-8")
                    self.assertEqual(self.invoke("apply", "bridge"), 1)
                    self.assertEqual(len(self.commands.calls), 0)

    def test_apply_rechecks_local_config_before_each_external_git_call(self):
        self.generated_checkout()
        self.add_patch()
        self.commands.tamper_config = '[filter "fixture"]\nprocess = fixture-command\n'
        self.assertEqual(self.invoke("apply", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 1)
        self.assertFalse(any("apply" in argv for argv, _ in self.commands.calls))

    def test_apply_refuses_unexpected_effective_worktree_before_status_or_apply(self):
        self.generated_checkout()
        self.add_patch()
        self.commands.effective_worktree = self.root / "outside"
        self.assertEqual(self.invoke("apply", "bridge"), 1)
        self.assertFalse(any("status" in argv or "apply" in argv for argv, _ in self.commands.calls))
        self.assertEqual(len(self.commands.calls), 1)

    def test_apply_refuses_metadata_redirection_and_external_object_alternates(self):
        checkout = self.generated_checkout()
        self.add_patch()
        for filename in ("commondir", "gitdir", "objects/info/alternates", "objects/info/http-alternates"):
            with self.subTest(filename=filename):
                pointer = checkout / ".git" / filename
                pointer.parent.mkdir(parents=True, exist_ok=True)
                pointer.write_text(str(self.root / "outside") + "\n", encoding="utf-8")
                try:
                    self.assertEqual(self.invoke("apply", "bridge"), 1)
                    self.assertEqual(len(self.commands.calls), 0)
                finally:
                    pointer.unlink()
                    self.commands.calls.clear()

    def test_apply_refuses_symlinked_metadata_children_before_git(self):
        checkout = self.generated_checkout()
        self.add_patch()
        outside = self.root / "outside"
        outside.mkdir()
        for name in ("refs", "logs", "info"):
            with self.subTest(name=name):
                link = checkout / ".git" / name
                link.symlink_to(outside, target_is_directory=True)
                try:
                    self.assertEqual(self.invoke("apply", "bridge"), 1)
                    self.assertEqual(len(self.commands.calls), 0)
                finally:
                    link.unlink()
                    self.commands.calls.clear()

    def test_apply_preserves_legitimate_partial_and_sparse_checkout_config(self):
        checkout = self.generated_checkout()
        self.add_patch()
        config = checkout / ".git/config"
        worktree_config = checkout / ".git/config.worktree"
        config.write_text(BASE_CONFIG + '[extensions]\nworktreeConfig = true\n[index]\nsparse = true\n',
                          encoding="utf-8")
        worktree_config.write_text('[core]\nsparseCheckout = true\nsparseCheckoutCone = false\n',
                                   encoding="utf-8")
        sparse_file = checkout / ".git/info/sparse-checkout"
        sparse_file.parent.mkdir()
        sparse_file.write_text("/*\n!/unused/\n", encoding="utf-8")
        before = {path: path.read_bytes() for path in (config, worktree_config, sparse_file)}
        self.assertEqual(self.invoke("apply", "bridge"), 0, self.err.getvalue())
        for path, content in before.items():
            self.assertEqual(path.read_bytes(), content)
        for argv, options in self.commands.calls:
            self.assertIn(f"--git-dir={checkout / '.git'}", argv)
            self.assertIn(f"--work-tree={checkout}", argv)
            self.assertEqual(options["env"]["GIT_OPTIONAL_LOCKS"], "0")
            self.assertEqual(options["env"]["GIT_CONFIG_GLOBAL"], "/dev/null")

    def test_all_checkout_commands_refuse_unsafe_config_without_git_or_manifest_writes(self):
        checkout = self.generated_checkout()
        self.add_patch()
        before = self.manifest_path.read_bytes()
        (checkout / ".git/config.worktree").write_text('[include]\npath = ../outside\n', encoding="utf-8")
        commands = [("status", "bridge"), ("fetch", "bridge"), ("apply", "bridge"), ("test", "bridge"),
                    ("export", "bridge", "--working-tree", "--patch", "bridge.patch")]
        for args in commands:
            with self.subTest(command=args[0]):
                self.assertEqual(self.invoke(*args), 1)
                self.assertEqual(len(self.commands.calls), 0)
                self.assertEqual(self.manifest_path.read_bytes(), before)
        self.assertIn("config", self.err.getvalue())

    def test_status_does_not_normalize_quoted_worktree_redirect_into_allowed_root(self):
        checkout = self.generated_checkout()
        (checkout / ".git/config").write_text(BASE_CONFIG + f'[core]\nworktree = "{checkout} "\n', encoding="utf-8")
        self.assertEqual(self.invoke("status", "bridge"), 1)
        self.assertEqual(len(self.commands.calls), 0)


if __name__ == "__main__":
    unittest.main()
