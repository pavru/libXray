"""Run: python3 build/test_build.py. No Go or platform build is run."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import unittest
from unittest.mock import call, patch
from uuid import uuid4

from app.android import AndroidBuilder
from app.desktop_core import DesktopCoreBuilder
from app.build import (
    DEFAULT_XRAY_CORE_VERSION,
    XRAY_CORE_REPOSITORY,
    resolve_xray_core_ref,
)

_TAG_OBJECT = "1" * 40
_TAG_COMMIT = "2" * 40
_BRANCH_COMMIT = "3" * 40


def _completed(stdout: str = "", returncode: int = 0):
    return subprocess.CompletedProcess([], returncode, stdout, "")


class ResolveXrayCoreRefTest(unittest.TestCase):
    def test_commits_and_go_versions_are_used_without_lookup(self):
        for ref in ("52a412d9e2f5", "52a412d9e2f5" * 3 + "abcd", DEFAULT_XRAY_CORE_VERSION,
                    "v1.260327.1"):
            with self.subTest(ref=ref), patch("app.build.subprocess.run") as run:
                self.assertEqual(resolve_xray_core_ref(ref), ref)
                run.assert_not_called()

    def test_release_tag_resolves_to_peeled_commit(self):
        listing = (f"{_TAG_OBJECT}\trefs/tags/v26.9.9\n"
                   f"{_TAG_COMMIT}\trefs/tags/v26.9.9^{{}}\n")
        with patch("app.build.subprocess.run", return_value=_completed(listing)) as run:
            self.assertEqual(resolve_xray_core_ref("v26.9.9"), _TAG_COMMIT)
        run.assert_called_once_with(
            ["git", "ls-remote", XRAY_CORE_REPOSITORY, "refs/tags/v26.9.9",
             "refs/tags/v26.9.9^{}", "refs/heads/v26.9.9"],
            capture_output=True, text=True,
        )

    def test_lightweight_tag_and_branch_resolve_to_their_commit(self):
        for listing, expected in (
            (f"{_TAG_COMMIT}\trefs/tags/main\n{_BRANCH_COMMIT}\trefs/heads/main\n", _TAG_COMMIT),
            (f"{_BRANCH_COMMIT}\trefs/heads/main\n", _BRANCH_COMMIT),
        ):
            with self.subTest(expected=expected), patch(
                "app.build.subprocess.run", return_value=_completed(listing)
            ):
                self.assertEqual(resolve_xray_core_ref("main"), expected)

    def test_unknown_ref_and_lookup_failure_are_rejected(self):
        for result in (_completed(""), _completed("", returncode=128)):
            with self.subTest(code=result.returncode), patch(
                "app.build.subprocess.run", return_value=result
            ), self.assertRaises(Exception):
                resolve_xray_core_ref("v99.0.0")


class BuildTest(unittest.TestCase):
    def setUp(self):
        self.root = (
            Path(__file__).resolve().parents[2]
            / "references"
            / "onexray-refactor-validation"
            / "build-scripts"
            / uuid4().hex
        )
        (self.root / "build").mkdir(parents=True)
        self.addCleanup(shutil.rmtree, self.root)
        self.builder = AndroidBuilder(str(self.root / "build"))

    def test_build_restores_modules_on_success_and_failure(self):
        for fails in (False, True):
            with self.subTest(fails=fails):
                (self.root / "go.mod").write_text("original module\n")
                (self.root / "go.sum").write_text("original sums\n")

                def prepare():
                    (self.root / "go.mod").write_text("effective module\n")
                    (self.root / "go.sum").write_text("effective sums\n")
                    if fails:
                        raise RuntimeError("original build failed")

                with (
                    patch.object(self.builder, "before_build", side_effect=prepare),
                    patch("app.android.os.chdir"),
                    patch("app.android.subprocess.run", return_value=subprocess.CompletedProcess([], 0)),
                ):
                    if fails:
                        with self.assertRaisesRegex(RuntimeError, "original build failed"):
                            self.builder.build()
                    else:
                        self.builder.build()

                self.assertEqual((self.root / "go.mod").read_text(), "original module\n")
                self.assertEqual((self.root / "go.sum").read_text(), "original sums\n")
                self.assertEqual(list((self.root / "build").iterdir()), [])
                self.assertIsNone(self.builder._go_env_snapshot)

    def test_gomobile_and_gobind_use_the_same_resolved_version(self):
        version = "v0.0.0-20260821190718-4776eadac327"
        for requested in ("", version):
            with self.subTest(requested=requested), patch.dict(
                "app.build.os.environ", {"LIBXRAY_GOMOBILE_VERSION": requested}
            ), patch(
                "app.build.subprocess.run",
                return_value=subprocess.CompletedProcess([], 0, version + "\n", ""),
            ) as run:
                self.builder.prepare_gomobile()
                self.assertEqual(run.call_args_list, [
                    call(["go", "list", "-m", "-f", "{{.Version}}",
                          f"golang.org/x/mobile@{requested or 'latest'}"],
                         capture_output=True, text=True),
                    call(["go", "get", "-tool", f"golang.org/x/mobile/cmd/gobind@{version}"]),
                    call(["go", "install", f"golang.org/x/mobile/cmd/gomobile@{version}"]),
                    call(["gomobile", "init"]),
                ])

    def _init_go_env(self, requested: str, resolved_version: str):
        (self.root / "go.mod").write_text("module\n")
        commands = []

        def run(command, **_):
            commands.append(command)
            if command[:2] == ["git", "ls-remote"]:
                return _completed(f"{_TAG_COMMIT}\trefs/tags/v26.9.9^{{}}\n")
            if command[:3] == ["go", "list", "-m"]:
                return _completed(resolved_version + "\n")
            return _completed()

        with patch.dict("app.build.os.environ", {"LIBXRAY_XRAY_CORE_REF": requested}), \
                patch("app.build.os.chdir"), patch("app.build.subprocess.run", side_effect=run):
            builder = AndroidBuilder(str(self.root / "build"))
            builder.prepare_xray_core()
            builder.init_go_env()
        metadata = json.loads((self.root / "xray-core.json").read_text(encoding="utf-8"))
        return commands, metadata

    def test_requested_ref_is_built_and_recorded(self):
        version = f"v1.260327.1-0.20260908222543-{_TAG_COMMIT[:12]}"
        (self.root / "xray-core.json").write_text("stale")

        commands, metadata = self._init_go_env("v26.9.9", version)

        self.assertIn(["go", "get", f"github.com/xtls/xray-core@{_TAG_COMMIT}"], commands)
        self.assertEqual(metadata, {"requestedRef": "v26.9.9", "local": False,
                                    "version": version, "revision": _TAG_COMMIT[:12]})

    def test_default_version_is_recorded_without_request(self):
        commands, metadata = self._init_go_env("", DEFAULT_XRAY_CORE_VERSION)

        self.assertIn(["go", "get", f"github.com/xtls/xray-core@{DEFAULT_XRAY_CORE_VERSION}"],
                      commands)
        self.assertFalse(any(command[:2] == ["git", "ls-remote"] for command in commands))
        self.assertEqual(metadata, {"requestedRef": None, "local": False,
                                    "version": DEFAULT_XRAY_CORE_VERSION,
                                    "revision": "52a412d9e2f5"})

    def test_desktop_core_cross_compiles_only_the_core(self):
        (self.root / "go.mod").write_text("original module\n")
        builder = DesktopCoreBuilder(str(self.root / "build"), "windows")
        seen = {}

        def build_bin(file_name):
            seen["file"] = file_name
            seen["goos"] = os.environ.get("GOOS")
            (self.root / "go.mod").write_text("effective module\n")

        with patch.dict("os.environ", {"GOOS": "linux"}), \
                patch.object(builder, "before_build") as before_build, \
                patch.object(builder, "build_desktop_bin", side_effect=build_bin):
            builder.build()
            self.assertEqual(os.environ["GOOS"], "linux")
        before_build.assert_called_once()
        self.assertEqual(seen, {"file": "xray.exe", "goos": "windows"})
        self.assertEqual((self.root / "go.mod").read_text(), "original module\n")
        with self.assertRaisesRegex(Exception, "not supported"):
            DesktopCoreBuilder(str(self.root / "build"), "android")

    def test_requested_ref_cannot_replace_local_checkout(self):
        with patch.dict("app.build.os.environ", {"LIBXRAY_XRAY_CORE_REF": "v26.9.9"}):
            builder = AndroidBuilder(str(self.root / "build"), use_local_xray_core=True)
        with self.assertRaisesRegex(Exception, "cannot be combined with local"):
            builder.prepare_xray_core()


if __name__ == "__main__":
    unittest.main()
