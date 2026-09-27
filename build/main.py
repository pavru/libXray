# main.py
import os
import sys

from app.android import AndroidBuilder
from app.apple_go import AppleGoBuilder
from app.apple_gomobile import AppleGoMobileBuilder
from app.build import resolve_xray_core_ref
from app.desktop_core import DesktopCoreBuilder
from app.linux import LinuxBuilder
from app.windows import WindowsBuilder

LOCAL_ARG = "local"
RESOLVE_XRAY_CORE_COMMAND = "resolve-xray-core"
DESKTOP_CORE_COMMAND = "core"


def build_dir_path():
    file_dir = os.path.dirname(__file__)
    dir_path = os.path.abspath(file_dir)
    return dir_path


def parse_local_arg(args: list[str]) -> bool:
    if not args:
        return False
    if args == [LOCAL_ARG]:
        return True
    raise Exception(f"unsupported args: {args}")


if __name__ == "__main__":
    if sys.argv[1:2] == [RESOLVE_XRAY_CORE_COMMAND]:
        # Prints only the resolved query so CI can capture it.
        if len(sys.argv) != 3 or not sys.argv[2].strip():
            raise Exception(f"usage: main.py {RESOLVE_XRAY_CORE_COMMAND} <ref>")
        print(resolve_xray_core_ref(sys.argv[2].strip()))
        sys.exit(0)

    print(sys.argv)
    platform = sys.argv[1]

    if platform == "apple":
        tool = sys.argv[2]
        use_local_xray_core = parse_local_arg(sys.argv[3:])
        if tool == "go":
            builder = AppleGoBuilder(build_dir_path(), use_local_xray_core)
            builder.build()
        elif tool == "gomobile":
            builder = AppleGoMobileBuilder(build_dir_path(), use_local_xray_core)
            builder.build()
        else:
            raise Exception(f"platform {platform} tool {tool} not supported")

    elif platform == "android":
        use_local_xray_core = parse_local_arg(sys.argv[2:])
        builder = AndroidBuilder(build_dir_path(), use_local_xray_core)
        builder.build()

    elif platform == "linux":
        use_local_xray_core = parse_local_arg(sys.argv[2:])
        builder = LinuxBuilder(build_dir_path(), use_local_xray_core)
        builder.build()

    elif platform == "windows":
        use_local_xray_core = parse_local_arg(sys.argv[2:])
        builder = WindowsBuilder(build_dir_path(), use_local_xray_core)
        builder.build()

    elif platform == DESKTOP_CORE_COMMAND:
        if len(sys.argv) < 3:
            raise Exception(f"usage: main.py {DESKTOP_CORE_COMMAND} <windows|linux> [local]")
        use_local_xray_core = parse_local_arg(sys.argv[3:])
        builder = DesktopCoreBuilder(build_dir_path(), sys.argv[2], use_local_xray_core)
        builder.build()

    else:
        raise Exception(f"platform {platform} not supported")
