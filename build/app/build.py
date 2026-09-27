import json
import os.path
import re
import subprocess

from app.cmd import (
    create_dir_if_not_exists,
    delete_file_if_exists,
    delete_dir_if_exists,
)

LIBXRAY_MOD_NAME = "github.com/xtls/libxray"
XRAY_CORE_MOD_NAME = "github.com/xtls/xray-core"
XRAY_CORE_REPOSITORY = "https://github.com/XTLS/Xray-core"
# Go modules resolve the Xray-core v26.9.9 release tag through this version.
DEFAULT_XRAY_CORE_VERSION = "v1.260327.1-0.20260908222543-52a412d9e2f5"
LOCAL_XRAY_CORE_DIR_NAME = "Xray-core"
XRAY_CORE_REF_ENV = "LIBXRAY_XRAY_CORE_REF"
XRAY_CORE_METADATA_FILE = "xray-core.json"

_COMMIT = re.compile(r"[0-9a-f]{7,40}")
# Xray-core release tags (v26.x) do not match the module path's major version,
# so only v0/v1 versions, including pseudo-versions, are valid go get queries.
_GO_VERSION = re.compile(r"v[01]\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?")
_PSEUDO_REVISION = re.compile(r"\d{14}-([0-9a-f]{12})$")


def resolve_xray_core_ref(ref: str) -> str:
    """Returns a go get query for an Xray-core tag, branch, commit or Go version."""
    if _COMMIT.fullmatch(ref) or _GO_VERSION.fullmatch(ref):
        return ref
    tag, branch = f"refs/tags/{ref}", f"refs/heads/{ref}"
    result = subprocess.run(
        ["git", "ls-remote", XRAY_CORE_REPOSITORY, tag, f"{tag}^{{}}", branch],
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise Exception(f"resolve xray-core ref failed: {ref}")
    commits = {}
    for line in result.stdout.splitlines():
        commit, _, name = line.partition("\t")
        commits[name] = commit
    # An annotated tag resolves to its peeled commit, not the tag object.
    for name in (f"{tag}^{{}}", tag, branch):
        if name in commits:
            return commits[name]
    raise Exception(f"xray-core ref not found: {ref}")


class Builder(object):
    def __init__(self, build_dir: str, use_local_xray_core: bool = False):
        self.build_dir = build_dir
        self.lib_dir = os.path.abspath(os.path.join(self.build_dir, ".."))
        self.use_local_xray_core = use_local_xray_core
        self.xray_core_replace_path = f"../{LOCAL_XRAY_CORE_DIR_NAME}"
        self.xray_core_dir = os.path.abspath(
            os.path.join(self.lib_dir, self.xray_core_replace_path)
        )
        self._go_env_snapshot = None
        self.xray_core_ref = os.environ.get(XRAY_CORE_REF_ENV, "").strip()

    def snapshot_go_env(self):
        paths = [
            os.path.join(self.lib_dir, "go.mod"),
            os.path.join(self.lib_dir, "go.sum"),
        ]
        snapshot = {}
        for path in paths:
            if not os.path.exists(path):
                snapshot[path] = None
                continue
            with open(path, "rb") as file:
                snapshot[path] = file.read()
        self._go_env_snapshot = snapshot

    def restore_go_env(self):
        if self._go_env_snapshot is None:
            return
        snapshot = self._go_env_snapshot
        for path, content in snapshot.items():
            if content is None:
                delete_file_if_exists(path)
                continue
            with open(path, "wb") as file:
                file.write(content)
        self._go_env_snapshot = None

    def clean_lib_files(self, files: list[str]):
        for file in files:
            file_path = os.path.join(self.lib_dir, file)
            delete_file_if_exists(file_path)

    def clean_lib_dirs(self, dirs: list[str]):
        for dir_name in dirs:
            dir_path = os.path.join(self.lib_dir, dir_name)
            delete_dir_if_exists(dir_path)

    def prepare_xray_core(self):
        # Never report metadata left over from an earlier build.
        delete_file_if_exists(os.path.join(self.lib_dir, XRAY_CORE_METADATA_FILE))
        if self.use_local_xray_core:
            if self.xray_core_ref:
                raise Exception(f"{XRAY_CORE_REF_ENV} cannot be combined with local")
            if not os.path.isdir(self.xray_core_dir):
                raise Exception(f"local Xray-core dir not found: {self.xray_core_dir}")

    def init_go_env(self):
        os.chdir(self.lib_dir)
        if not os.path.exists(os.path.join(self.lib_dir, "go.mod")):
            ret = subprocess.run(["go", "mod", "init", LIBXRAY_MOD_NAME])
            if ret.returncode != 0:
                raise Exception("go mod init failed")

        if self.use_local_xray_core:
            ret = subprocess.run(
                [
                    "go",
                    "mod",
                    "edit",
                    f"-replace={XRAY_CORE_MOD_NAME}={self.xray_core_replace_path}",
                ]
            )
            if ret.returncode != 0:
                raise Exception("go mod edit replace failed")
        else:
            ret = subprocess.run(
                ["go", "mod", "edit", f"-dropreplace={XRAY_CORE_MOD_NAME}"]
            )
            if ret.returncode != 0:
                raise Exception("go mod edit dropreplace failed")

            version = (
                resolve_xray_core_ref(self.xray_core_ref)
                if self.xray_core_ref
                else DEFAULT_XRAY_CORE_VERSION
            )
            ret = subprocess.run(["go", "get", f"{XRAY_CORE_MOD_NAME}@{version}"])
            if ret.returncode != 0:
                raise Exception("go get xray-core failed")

        ret = subprocess.run(
            [
                "go",
                "mod",
                "tidy",
            ]
        )
        if ret.returncode != 0:
            raise Exception("go mod tidy failed")
        self.write_xray_core_metadata()

    def write_xray_core_metadata(self):
        """Records the Xray-core build input; go.mod is restored after the build."""
        if self.use_local_xray_core:
            version = "local"
            result = subprocess.run(
                ["git", "rev-parse", "HEAD"],
                cwd=self.xray_core_dir,
                capture_output=True,
                text=True,
            )
            revision = result.stdout.strip() if result.returncode == 0 else None
        else:
            result = subprocess.run(
                ["go", "list", "-m", "-f", "{{.Version}}", XRAY_CORE_MOD_NAME],
                capture_output=True,
                text=True,
            )
            version = result.stdout.strip()
            if result.returncode != 0 or not version:
                raise Exception("resolve xray-core module version failed")
            match = _PSEUDO_REVISION.search(version)
            revision = match.group(1) if match else None
        metadata = {
            "requestedRef": self.xray_core_ref or None,
            "local": self.use_local_xray_core,
            "version": version,
            "revision": revision,
        }
        path = os.path.join(self.lib_dir, XRAY_CORE_METADATA_FILE)
        with open(path, "w", encoding="utf-8") as file:
            json.dump(metadata, file, indent=2)
            file.write("\n")

    def download_geo(self):
        os.chdir(self.lib_dir)
        main_path = os.path.join("download_geo", "main.go")
        ret = subprocess.run(["go", "run", main_path])
        if ret.returncode != 0:
            raise Exception("download_geo failed")

    def prepare_gomobile(self):
        requested_version = os.environ.get("LIBXRAY_GOMOBILE_VERSION") or "latest"
        result = subprocess.run(
            [
                "go",
                "list",
                "-m",
                "-f",
                "{{.Version}}",
                f"golang.org/x/mobile@{requested_version}",
            ],
            capture_output=True,
            text=True,
        )
        version = result.stdout.strip()
        if result.returncode != 0 or not version:
            raise Exception("resolve gomobile version failed")

        ret = subprocess.run(
            [
                "go",
                "get",
                "-tool",
                f"golang.org/x/mobile/cmd/gobind@{version}",
            ]
        )
        if ret.returncode != 0:
            raise Exception("add gobind tool dependency failed")

        ret = subprocess.run(
            [
                "go",
                "install",
                f"golang.org/x/mobile/cmd/gomobile@{version}",
            ]
        )
        if ret.returncode != 0:
            raise Exception("go install gomobile failed")
        ret = subprocess.run(["gomobile", "init"])
        if ret.returncode != 0:
            raise Exception("gomobile init failed")

    def prepare_static_lib(self):
        main_file = os.path.join(self.lib_dir, "cgo_bridge", "main.go")
        if not os.path.isfile(main_file):
            raise Exception("cgo bridge entrypoint is missing")

    def main_package(self) -> str:
        return "./cgo_bridge"

    def build_desktop_bin(self, file_name: str):
        output_dir = os.path.join(self.lib_dir, "bin")
        create_dir_if_not_exists(output_dir)
        output_file = os.path.join(output_dir, file_name)
        run_env = os.environ.copy()
        run_env["CGO_ENABLED"] = "0"
        cmd = [
            "go",
            "build",
            "-trimpath",
            "-buildvcs=false",
            "-ldflags",
            "-s -w -buildid=",
            f"-o={output_file}",
            "./desktop_bin",
        ]
        print(cmd)
        ret = subprocess.run(cmd, cwd=self.lib_dir, env=run_env)
        if ret.returncode != 0:
            raise Exception("build_desktop_bin failed")

    def before_build(self):
        self.prepare_xray_core()
        self.init_go_env()
        self.download_geo()

    def build(self):
        pass
