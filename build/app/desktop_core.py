import os

from app.build import Builder

DESKTOP_CORE_FILES = {"windows": "xray.exe", "linux": "xray"}


class DesktopCoreBuilder(Builder):
    """Builds only the desktop session Core, without the cgo library.

    The Core is a pure Go binary, so it cross-compiles for the target from any
    host; GOARCH comes from the environment.
    """

    def __init__(self, build_dir: str, target_os: str, use_local_xray_core: bool = False):
        super().__init__(build_dir, use_local_xray_core)
        if target_os not in DESKTOP_CORE_FILES:
            raise Exception(f"desktop core os {target_os} not supported")
        self.target_os = target_os

    def before_build(self):
        # Geo data belongs to app bundles, not to a standalone Core.
        self.prepare_xray_core()
        self.init_go_env()

    def build(self):
        self.snapshot_go_env()
        previous_goos = os.environ.get("GOOS")
        try:
            self.before_build()
            os.environ["GOOS"] = self.target_os
            self.build_desktop_bin(DESKTOP_CORE_FILES[self.target_os])
        finally:
            if previous_goos is None:
                os.environ.pop("GOOS", None)
            else:
                os.environ["GOOS"] = previous_goos
            self.restore_go_env()
