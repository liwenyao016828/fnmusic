#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
fnOS（飞牛 NAS）FPK 打包脚本 —— 零第三方依赖，仅用 Python 标准库。

背景
----
本仓库 README 提到的 `scripts/fnpack.exe` 并不存在于仓库中，因此无法依赖官方
fnpack 工具。但仓库内的 `fn-lx-player.fpk` 已确认就是**标准 gzip 压缩的 tar**，
其内部结构可用 `tarfile` / `gzip` 完整复刻，故本脚本直接产出 fnOS 可安装的 fpk。

已复刻的既有 fpk 事实（通过逐字节勘察得出，见 --inspect 输出）
------------------------------------------------------------
* 外层 `.fpk`：gzip（mtime=0、OS=0xff、XFL=0 → 等价 Go 默认压缩级别）
  tar 格式为 **POSIX ustar**（magic `ustar\\0` + 版本 `00`），无 PAX/GNU 扩展头，
  结尾仅 1024 字节零块（不做 10240 字节记录对齐）。
* 成员顺序：
    外层 : app.tgz, cmd/(+9 个脚本), config/(privilege, resource),
           ICON.PNG, ICON_256.PNG, manifest, wizard/(空目录)
    app.tgz: <appname>, ui/, ui/config, ui/images/(+PNG), config/(privilege, resource)
      ↑ 主程序与图标名都取自 `fpk-package/manifest` 的 `appname`（见 read_manifest_appname）
* 所有成员 uid=gid=0、uname=gname 为空、mtime 统一为同一打包时间戳。
* 外层 `manifest` 是被**重新格式化**过的版本：`key` 左对齐补空格到 20 列，
  再 `" = "` + 值，**行尾为 CRLF**，并在最后追加一行
  `checksum = <app.tgz 的 md5>`。本脚本按同样规则生成。

权限策略
--------
原 fpk 里所有文件是 0666、目录是 0777（该 fpk 由 Windows 上的工具生成，
Windows 无 Unix 权限位）。`cmd/install_callback` 本身会 `chmod +x`，
所以 0666 也能装上，但缺少可执行位并不规范。
本脚本默认采用规范 Unix 权限：
    目录 0755 / 普通文件 0644 / `cmd/*` 与主程序 `<appname>` 0755
如需与原 fpk 完全一致的权限位，使用 `--legacy-modes`。

用法
----
    python3 scripts/build_fpk.py [--version 2.0.0] [--skip-frontend]
                                [--output <appname>.fpk] [--go /path/to/go]
                                [--legacy-modes] [--inspect <fpk>]

输出文件名默认等于 manifest 的 `appname` + `.fpk`。

退出码：0 成功；非 0 表示构建或自检失败（错误信息为中文）。
"""

from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import os
import shutil
import struct
import subprocess
import sys
import tarfile
import time
from pathlib import Path

# ---------------------------------------------------------------------------
# 常量
# ---------------------------------------------------------------------------

SCRIPT_DIR = Path(__file__).resolve().parent
ROOT = SCRIPT_DIR.parent
FPK_DIR = ROOT / "fpk-package"
APP_DIR = FPK_DIR / "app"
FRONTEND_DIR = ROOT / "frontend"
BACKEND_DIR = ROOT / "backend"
DIST_DIR = BACKEND_DIR / "dist"
TOOLS_DIR = ROOT / ".tools"
NPM_CACHE_DIR = ROOT / ".npm-cache"

APP_TGZ_NAME = "app.tgz"


def read_manifest_appname(manifest_path: Path) -> str:
    """从 `fpk-package/manifest` 读 `appname` —— 应用标识的**单一数据源**。

    改应用名只需改 manifest 一处：二进制名、输出 `.fpk` 名、自检清单都跟着走。
    （Go module 名 `fn-lx-player` 是 import 路径，与应用标识无关，不要动。）
    """
    try:
        for line in manifest_path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line.startswith("appname="):
                value = line.split("=", 1)[1].strip()
                if value:
                    return value
    except OSError:
        pass
    return "fn-lx-player"


APP_NAME = read_manifest_appname(FPK_DIR / "manifest")
DEFAULT_OUTPUT = ROOT / f"{APP_NAME}.fpk"

# 规范权限（--legacy-modes 时改为 0666 / 0777，与原 fpk 一致）
MODE_DIR = 0o755
MODE_FILE = 0o644
MODE_EXEC = 0o755
MODE_DIR_LEGACY = 0o777
MODE_FILE_LEGACY = 0o666
MODE_EXEC_LEGACY = 0o666

GZIP_LEVEL = 6          # 与原 fpk 的 XFL=0（非 1、非 9）一致
MANIFEST_KEY_WIDTH = 21  # 原 fpk manifest 的 key 左对齐宽度（= 最长 key 长度）

CMD_FILES = [
    "config_callback",
    "config_init",
    "install_callback",
    "install_init",
    "main",
    "uninstall_callback",
    "uninstall_init",
    "upgrade_callback",
    "upgrade_init",
]

# 既有 fpk 的成员集合，用于打包后结构比对
REF_OUTER = [
    "app.tgz", "cmd", "cmd/config_callback", "cmd/config_init",
    "cmd/install_callback", "cmd/install_init", "cmd/main",
    "cmd/uninstall_callback", "cmd/uninstall_init", "cmd/upgrade_callback",
    "cmd/upgrade_init", "config", "config/privilege", "config/resource",
    "ICON.PNG", "ICON_256.PNG", "manifest", "wizard",
]
REF_INNER = [
    APP_NAME, "ui", "ui/config", "ui/images",
    "ui/images/ICON.PNG", f"ui/images/{APP_NAME}-256.png",
    f"ui/images/{APP_NAME}.png", "ui/images/icon_128.png",
    "ui/images/icon_16.png", "ui/images/icon_20.png", "ui/images/icon_24.png",
    "ui/images/icon_256.png", "ui/images/icon_32.png", "ui/images/icon_48.png",
    "ui/images/icon_512.png", "ui/images/icon_64.png", "ui/images/icon_72.png",
    "ui/images/icon_96.png", "config", "config/privilege", "config/resource",
    # musicdl sidecar 的 Python 源码（v2.1.83 起）。单一来源在仓库根的
    # sidecar/musicdl_service/，构建时复制进来 —— 不放进 fpk-package/app/，
    # 免得同一份源码有两处、各自漂移。
    "sidecar", "sidecar/musicdl_service",
    "sidecar/musicdl_service/app.py", "sidecar/musicdl_service/core.py",
    "sidecar/musicdl_service/requirements.txt",
]

MIN_BINARY_SIZE = 1024 * 1024  # 自检阈值：主程序必须 > 1MB

# ---------------------------------------------------------------------------
# 小工具
# ---------------------------------------------------------------------------


def info(msg: str) -> None:
    print(f"[信息] {msg}", flush=True)


def step(msg: str) -> None:
    print(f"\n=== {msg} ===", flush=True)


def die(msg: str) -> "None":
    print(f"\n[错误] {msg}", file=sys.stderr, flush=True)
    sys.exit(1)


def human(n: int) -> str:
    return f"{n} 字节 ({n / 1024 / 1024:.2f} MiB)"


# ---------------------------------------------------------------------------
# 1. 前端构建
# ---------------------------------------------------------------------------


def run_frontend_build() -> None:
    step("1/5 构建前端（npm run build）")
    npm = shutil.which("npm") or shutil.which("npm.cmd")
    if not npm:
        die("未找到 npm，请先安装 Node.js 18+，或加 --skip-frontend 跳过前端构建。")
    env = dict(os.environ)
    if NPM_CACHE_DIR.is_dir():
        env.setdefault("npm_config_cache", str(NPM_CACHE_DIR))
    info(f"执行: {npm} run build  (cwd={FRONTEND_DIR})")
    proc = subprocess.run([npm, "run", "build"], cwd=str(FRONTEND_DIR), env=env)
    if proc.returncode != 0:
        die(f"前端构建失败（npm run build 退出码 {proc.returncode}）。")
    info("前端构建完成。")


# ---------------------------------------------------------------------------
# 2. Go 交叉编译
# ---------------------------------------------------------------------------


def _go_names() -> tuple[str, ...]:
    """Go 可执行文件名：Windows 上带 .exe 后缀。"""
    return ("go.exe", "go") if os.name == "nt" else ("go",)


def _expand_go_path(raw: str) -> list[Path]:
    """展开用户给定的 go 路径；Windows 下若缺 .exe 后缀则自动补一个候选。

    Path.is_file() 不像 shell 那样自动补 .exe，因此必须显式处理，
    否则 `--go .tools/go/bin/go` 在 Windows 上会被判为不存在。
    """
    p = Path(raw)
    if os.name == "nt" and p.suffix.lower() != ".exe":
        return [p, Path(str(p) + ".exe")]
    return [p]


def resolve_go(go_arg: str | None) -> Path:
    """按 --go → 环境变量 GO → 仓库内置 .tools/go → PATH 的顺序探测 go。"""
    candidates: list[Path] = []
    if go_arg:
        candidates.extend(_expand_go_path(go_arg))
    if os.environ.get("GO"):
        candidates.extend(_expand_go_path(os.environ["GO"]))
    for name in _go_names():
        candidates.append(TOOLS_DIR / "go" / "bin" / name)
    which = shutil.which("go")
    if which:
        candidates.append(Path(which))

    for cand in candidates:
        if not cand.is_file():
            continue
        # Windows 无 X_OK 语义（os.access 对任意已存在文件都返回 True），仅在 POSIX 上校验
        if os.name != "nt" and not os.access(cand, os.X_OK):
            continue
        return cand.resolve()
    bundled = " 或 ".join(str(TOOLS_DIR / "go" / "bin" / n) for n in _go_names())
    die(
        "未找到 Go 工具链。请用 --go 指定 go 可执行文件，"
        f"或确保 {bundled} 存在，或把 go 加入 PATH。"
    )
    raise SystemExit(1)  # pragma: no cover


def go_env(go_exe: Path) -> dict:
    env = dict(os.environ)
    env["GOOS"] = "linux"
    env["GOARCH"] = "amd64"
    env["CGO_ENABLED"] = "0"
    # GOROOT 从 go 可执行文件位置推导（<goroot>/bin/go）
    if not env.get("GOROOT"):
        goroot = go_exe.parent.parent
        if (goroot / "src").is_dir() or (goroot / "pkg").is_dir():
            env["GOROOT"] = str(goroot)
            env["PATH"] = str(go_exe.parent) + os.pathsep + env.get("PATH", "")
    # 仓库内缓存的 GOCACHE/GOPATH，避免污染 $HOME
    if not env.get("GOCACHE") and (TOOLS_DIR / "gocache").is_dir():
        env["GOCACHE"] = str(TOOLS_DIR / "gocache")
    if not env.get("GOPATH") and (TOOLS_DIR / "gopath").is_dir():
        env["GOPATH"] = str(TOOLS_DIR / "gopath")
    # 模块代理：项目已引入第三方依赖（modernc.org/sqlite），而默认的 proxy.golang.org
    # 在部分网络环境下不可达。允许用环境变量 GOPROXY 覆盖；未设置时给一个国内可用的默认值。
    # 首次构建会下载模块到 GOPATH/pkg/mod，之后即可离线构建。
    env.setdefault("GOPROXY", "https://goproxy.cn,direct")
    # 禁止 Go 自动下载新工具链：否则依赖升级会把 go.mod 的 go 指令顶高，导致构建环境不一致
    env.setdefault("GOTOOLCHAIN", "local")
    return env


def run_go_build(go_exe: Path, out_bin: Path) -> None:
    step("2/5 交叉编译 Linux amd64 主程序")
    env = go_env(go_exe)
    # ⚠️ 编译**包**（`.`）而不是 `main.go` 这一个文件。
    # 写死文件名时，`main` 包里的**其它文件**（如 musicdl_wiring.go）会被静默漏掉，
    # 现象是「本地 go build ./... 一切正常，打包时才 undefined」—— 真撞过。
    cmd = [
        str(go_exe), "build", "-ldflags=-s -w",
        "-o", str(out_bin), ".",
    ]
    info(f"GOOS={env.get('GOOS')} GOARCH={env.get('GOARCH')} CGO_ENABLED={env.get('CGO_ENABLED')}")
    info(f"执行: {' '.join(cmd)}  (cwd={BACKEND_DIR})")
    proc = subprocess.run(cmd, cwd=str(BACKEND_DIR), env=env)
    if proc.returncode != 0:
        die(f"后端编译失败（go build 退出码 {proc.returncode}）。")
    if not out_bin.is_file():
        die(f"后端编译未产出预期文件：{out_bin}")
    info(f"编译完成：{out_bin}  {human(out_bin.stat().st_size)}")


# ---------------------------------------------------------------------------
# 3. 打包前校验
# ---------------------------------------------------------------------------


def check_required(skip_frontend: bool, binary: Path) -> None:
    step("3/5 打包前必需文件校验")

    missing: list[str] = []

    def need(p: Path, desc: str) -> None:
        if not p.exists():
            missing.append(f"  - {p.relative_to(ROOT) if p.is_relative_to(ROOT) else p}  ({desc})")

    need(FPK_DIR / "manifest", "应用清单，定义 appname/version/端口/桌面入口")
    for icon in ("ICON.PNG", "ICON_256.PNG"):
        need(FPK_DIR / icon, f"应用中心图标 {icon}")
    for c in CMD_FILES:
        need(FPK_DIR / "cmd" / c, "生命周期脚本 cmd/*")
    need(FPK_DIR / "config" / "privilege", "权限配置")
    need(FPK_DIR / "config" / "resource", "数据目录配置")
    need(APP_DIR / "ui" / "config", "桌面入口 ui/config")
    need(APP_DIR / "ui" / "images", "桌面图标目录 ui/images")
    need(binary, "主程序（Go 构建产物）")

    if not DIST_DIR.is_dir():
        missing.append(
            f"  - {DIST_DIR.relative_to(ROOT)}/  (前端构建产物；"
            + ("请去掉 --skip-frontend 重新构建" if skip_frontend else "请先执行前端构建")
            + ")"
        )
    elif not (DIST_DIR / "index.html").is_file():
        missing.append(f"  - {DIST_DIR.relative_to(ROOT)}/index.html  (前端构建产物不完整)")

    if missing:
        die(
            "以下必需文件/目录缺失，无法打包：\n"
            + "\n".join(missing)
            + "\n\n请先补齐上述文件（前端产物：在 frontend/ 执行 npm run build；"
            "主程序：本脚本会自动 go build 生成）。"
        )

    icons = sorted(p.name for p in (APP_DIR / "ui" / "images").iterdir() if p.is_file())
    if not icons:
        die(f"{APP_DIR / 'ui' / 'images'} 为空，至少需要一个桌面图标。")
    if not (DIST_DIR / "assets").is_dir():
        print(f"[警告] 未找到 {DIST_DIR / 'assets'}，前端静态资源可能不完整。", flush=True)
    info(f"校验通过（桌面图标 {len(icons)} 个，cmd 脚本 {len(CMD_FILES)} 个）。")


# ---------------------------------------------------------------------------
# 4. 组装 tar / gzip
# ---------------------------------------------------------------------------


def trim_to_go_trailer(buf: bytes) -> bytes:
    """
    Python 的 tarfile 会把结尾补齐到 10240 字节记录边界，
    而原 fpk（Go archive/tar 生成）只写 1024 字节零块。
    这里裁掉多余补零，使尾部结构与原 fpk 一致。
    """
    # 从尾部向前找最后一个非零 512 块
    n_blocks = len(buf) // 512
    last = 0
    for i in range(n_blocks - 1, -1, -1):
        blk = buf[i * 512:(i + 1) * 512]
        if blk.strip(b"\0"):
            last = i
            break
    return buf[: (last + 1) * 512 + 1024]


class TarBuilder:
    """按固定 mtime/uid/gid/uname/gname 生成 POSIX ustar 归档。"""

    def __init__(self, mtime: int, dir_mode: int, file_mode: int) -> None:
        self.mtime = mtime
        self.dir_mode = dir_mode
        self.file_mode = file_mode
        self.buf = io.BytesIO()
        self._tf = tarfile.open(fileobj=self.buf, mode="w", format=tarfile.USTAR_FORMAT)

    def _base(self, name: str, mode: int, type_: bytes, size: int = 0) -> tarfile.TarInfo:
        ti = tarfile.TarInfo(name)
        ti.type = type_
        ti.mode = mode
        ti.uid = 0
        ti.gid = 0
        ti.uname = ""
        ti.gname = ""
        ti.mtime = self.mtime
        ti.size = size
        return ti

    def add_dir(self, name: str) -> None:
        self._tf.addfile(self._base(name, self.dir_mode, tarfile.DIRTYPE))

    def add_file(self, name: str, src: Path, mode: int | None = None) -> None:
        data = src.read_bytes()
        self.add_bytes(name, data, self.file_mode if mode is None else mode)

    def add_bytes(self, name: str, data: bytes, mode: int) -> None:
        ti = self._base(name, mode, tarfile.REGTYPE, size=len(data))
        self._tf.addfile(ti, io.BytesIO(data))

    def finish(self) -> bytes:
        self._tf.close()
        return trim_to_go_trailer(self.buf.getvalue())


def gz_bytes(data: bytes) -> bytes:
    """gzip 压缩：mtime=0、不含文件名（FLG=0）、level=6 —— 与原 fpk 头一致。"""
    out = io.BytesIO()
    with gzip.GzipFile(fileobj=out, mode="wb", compresslevel=GZIP_LEVEL, mtime=0) as gz:
        gz.write(data)
    return out.getvalue()


def build_manifest_bytes(source: Path, version: str | None, app_tgz: bytes) -> bytes:
    """复刻 fnpack 的 manifest 重写规则：key 补到 20 列 + ' = ' + 值，CRLF，追加 checksum。"""
    items: list[tuple[str, str]] = []
    for raw in source.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        k, v = line.split("=", 1)
        items.append((k.strip(), v.strip()))

    keys = {k for k, _ in items}
    if version:
        items = [(k, version if k == "version" else v) for k, v in items]
        if "version" not in keys:
            items.insert(1, ("version", version))

    checksum = hashlib.md5(app_tgz).hexdigest()
    items.append(("checksum", checksum))

    # 原 fpk 的 key 左对齐宽度 = 最长 key 长度（本仓库为 21，即 desktop_applaunchname）
    width = max(MANIFEST_KEY_WIDTH, max(len(k) for k, _ in items))
    lines = [f"{k:<{width}} = {v}" for k, v in items]
    return ("\r\n".join(lines) + "\r\n").encode("utf-8")


def build_app_tgz(mtime: int, legacy: bool) -> bytes:
    step("组装 app.tgz（内层应用包）")
    file_mode = MODE_FILE_LEGACY if legacy else MODE_FILE
    dir_mode = MODE_DIR_LEGACY if legacy else MODE_DIR
    exec_mode = MODE_EXEC_LEGACY if legacy else MODE_EXEC

    tb = TarBuilder(mtime, dir_mode, file_mode)
    # 顺序与原 fpk 一致：主程序 → ui/ → config/
    tb.add_file(APP_NAME, APP_DIR / APP_NAME, exec_mode)
    tb.add_dir("ui")
    tb.add_file("ui/config", APP_DIR / "ui" / "config")
    tb.add_dir("ui/images")
    for img in sorted(p for p in (APP_DIR / "ui" / "images").iterdir() if p.is_file()):
        tb.add_file(f"ui/images/{img.name}", img)
    tb.add_dir("config")
    for cfg in sorted(p for p in (FPK_DIR / "config").iterdir() if p.is_file()):
        tb.add_file(f"config/{cfg.name}", cfg)

    # sidecar/：musicdl 的 Python 源码（v2.1.83 起）。
    #
    # **显式列文件名**而不是扫目录：扫目录会把 __pycache__/*.pyc 和 test_*.py
    # 一起打进去（前者是噪声、后者会让应用包多出几万字节的测试代码）。
    # 少一个文件在这里是**静默失败**（真机上表现为「找不到 sidecar 源码目录」），
    # 所以下面还有一条存在性断言。
    sidecar_src = ROOT / "sidecar" / "musicdl_service"
    if sidecar_src.is_dir():
        tb.add_dir("sidecar")
        tb.add_dir("sidecar/musicdl_service")
        for name in ("app.py", "core.py", "requirements.txt"):
            src = sidecar_src / name
            if not src.is_file():
                die(f"sidecar 源码缺文件：{src}（打包会静默少一个，装到真机上才发现）")
            tb.add_file(f"sidecar/musicdl_service/{name}", src)
    else:
        die(f"找不到 sidecar 源码目录：{sidecar_src}")

    tar_bytes = tb.finish()
    tgz = gz_bytes(tar_bytes)
    info(f"app.tgz 生成完毕：tar {human(len(tar_bytes))} → gzip {human(len(tgz))}")
    return tgz


def build_fpk_bytes(app_tgz: bytes, mtime: int, legacy: bool, version: str | None) -> bytes:
    step("组装外层 .fpk")
    file_mode = MODE_FILE_LEGACY if legacy else MODE_FILE
    dir_mode = MODE_DIR_LEGACY if legacy else MODE_DIR
    exec_mode = MODE_EXEC_LEGACY if legacy else MODE_EXEC

    tb = TarBuilder(mtime, dir_mode, file_mode)
    tb.add_bytes(APP_TGZ_NAME, app_tgz, file_mode)

    tb.add_dir("cmd")
    for c in sorted(p for p in (FPK_DIR / "cmd").iterdir() if p.is_file()):
        tb.add_file(f"cmd/{c.name}", c, exec_mode)

    tb.add_dir("config")
    for cfg in sorted(p for p in (FPK_DIR / "config").iterdir() if p.is_file()):
        tb.add_file(f"config/{cfg.name}", cfg)

    for icon in ("ICON.PNG", "ICON_256.PNG"):
        tb.add_file(icon, FPK_DIR / icon)

    tb.add_bytes("manifest", build_manifest_bytes(FPK_DIR / "manifest", version, app_tgz), file_mode)

    # wizard/ 空目录（打包材料里没有，必须显式创建）
    wizard = FPK_DIR / "wizard"
    tb.add_dir("wizard")
    if wizard.is_dir():
        for sub in sorted(wizard.rglob("*")):
            rel = sub.relative_to(FPK_DIR).as_posix()
            if sub.is_dir():
                tb.add_dir(rel)
            else:
                tb.add_file(rel, sub)

    tar_bytes = tb.finish()
    fpk = gz_bytes(tar_bytes)
    info(f"外层 fpk 生成完毕：tar {human(len(tar_bytes))} → gzip {human(len(fpk))}")
    return fpk


# ---------------------------------------------------------------------------
# 5. 打包后自检
# ---------------------------------------------------------------------------


def read_elf_machine(data: bytes) -> tuple[str, str] | None:
    """返回 (class, machine) 描述；非 ELF 返回 None。"""
    if len(data) < 20 or data[:4] != b"\x7fELF":
        return None
    ei_class = data[4]
    e_machine = struct.unpack_from("<H", data, 18)[0]
    cls = {1: "ELF32", 2: "ELF64"}.get(ei_class, f"class={ei_class}")
    mach = {0x3E: "x86-64", 0x03: "i386", 0xB7: "AArch64", 0x28: "ARM"}.get(
        e_machine, f"machine={e_machine}"
    )
    return cls, mach


def list_members(data: bytes) -> list[tarfile.TarInfo]:
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as t:
        return t.getmembers()


def print_structure(fpk_bytes: bytes) -> None:
    print("\n--- 生成的 fpk 完整成员结构 ---", flush=True)
    print(f"{'成员名':<48} {'类型':<6} {'模式':<7} {'uid/gid':<8} {'大小':>10}", flush=True)
    with tarfile.open(fileobj=io.BytesIO(fpk_bytes), mode="r:gz") as t:
        for m in t.getmembers():
            kind = "dir" if m.isdir() else ("file" if m.isfile() else m.type.decode("latin1"))
            print(f"{m.name:<48} {kind:<6} {oct(m.mode):<7} {m.uid}/{m.gid:<5} {m.size:>10}", flush=True)
        app_member = t.getmember(APP_TGZ_NAME)
        app_tgz = t.extractfile(app_member).read()

    print("\n--- app.tgz 内部成员结构 ---", flush=True)
    print(f"{'成员名':<48} {'类型':<6} {'模式':<7} {'uid/gid':<8} {'大小':>10}", flush=True)
    for m in list_members(app_tgz):
        kind = "dir" if m.isdir() else ("file" if m.isfile() else m.type.decode("latin1"))
        print(f"{m.name:<48} {kind:<6} {oct(m.mode):<7} {m.uid}/{m.gid:<5} {m.size:>10}", flush=True)


def self_check(fpk_path: Path, legacy: bool = False) -> None:
    step("5/5 打包后自检")

    errors: list[str] = []
    if not fpk_path.is_file():
        die(f"未生成 fpk 文件：{fpk_path}")
    fpk_bytes = fpk_path.read_bytes()
    info(f"fpk 大小：{human(len(fpk_bytes))}")

    # gzip 头（应为 mtime=0 / OS=0xff）
    gz_mtime = struct.unpack_from("<I", fpk_bytes, 4)[0]
    info(f"gzip 头：FLG=0x{fpk_bytes[3]:02x} mtime={gz_mtime} XFL=0x{fpk_bytes[8]:02x} OS=0x{fpk_bytes[9]:02x}")

    with tarfile.open(fileobj=io.BytesIO(fpk_bytes), mode="r:gz") as t:
        outer_names = [m.name for m in t.getmembers()]
        names = set(outer_names)
        if APP_TGZ_NAME not in names:
            die("生成的 fpk 中缺少 app.tgz！")
        app_tgz = t.extractfile(APP_TGZ_NAME).read()
        if "manifest" not in names:
            errors.append("外层缺少 manifest")
        else:
            man = t.extractfile("manifest").read().decode("utf-8")
            if "checksum" not in man:
                errors.append("外层 manifest 缺少 checksum 行")
            elif hashlib.md5(app_tgz).hexdigest() not in man:
                errors.append("外层 manifest 的 checksum 与 app.tgz 不匹配")
        if "wizard" not in names:
            errors.append("外层缺少 wizard/ 空目录")
        elif not t.getmember("wizard").isdir():
            errors.append("wizard 不是目录")

    inner_names = [m.name for m in list_members(app_tgz)]
    inner_set = set(inner_names)

    # 主程序：存在、> 1MB、ELF x86-64
    if APP_NAME not in inner_set:
        die(f"app.tgz 中缺少主程序 {APP_NAME}！")
    binary = None
    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:gz") as at:
        binary = at.extractfile(APP_NAME).read()
    if len(binary) <= MIN_BINARY_SIZE:
        errors.append(f"主程序过小：{human(len(binary))}，应大于 1 MiB")
    elf = read_elf_machine(binary)
    if elf is None:
        errors.append("主程序不是 ELF 可执行文件")
    else:
        cls, mach = elf
        info(f"主程序：{human(len(binary))}，{cls} {mach}")
        if mach != "x86-64" or cls != "ELF64":
            errors.append(f"主程序架构不正确：期望 ELF64 x86-64，实际 {cls} {mach}")

    # 权限检查（--legacy-modes 刻意复刻原 fpk 的无执行位状态，故只警告不报错。
    # 原 fpk 之所以能装上，是因为 cmd/install_callback 会自行 chmod +x。）
    noexec: list[str] = []
    with tarfile.open(fileobj=io.BytesIO(fpk_bytes), mode="r:gz") as t:
        for m in t.getmembers():
            if m.name.startswith("cmd/") and m.isfile() and not (m.mode & 0o111):
                noexec.append(f"{m.name} (mode={oct(m.mode)})")
    with tarfile.open(fileobj=io.BytesIO(app_tgz), mode="r:gz") as at:
        bm = at.getmember(APP_NAME)
        if not (bm.mode & 0o111):
            noexec.append(f"app.tgz:{APP_NAME} (mode={oct(bm.mode)})")
    if noexec:
        if legacy:
            print(f"[警告] --legacy-modes：以下成员没有可执行位（与原 fpk 一致，"
                  f"依赖 cmd/install_callback 的 chmod +x）：{', '.join(noexec)}", flush=True)
        else:
            errors.extend(f"{n} 缺少可执行位" for n in noexec)

    # 结构比对
    ref_outer, ref_inner = set(REF_OUTER), set(REF_INNER)
    miss_o, extra_o = sorted(ref_outer - names), sorted(names - ref_outer)
    miss_i, extra_i = sorted(ref_inner - inner_set), sorted(inner_set - ref_inner)
    order_ok = outer_names == REF_OUTER and inner_names == REF_INNER

    print("\n--- 与原 fpk 结构比对 ---", flush=True)
    print(f"外层成员集合：{'一致' if not (miss_o or extra_o) else '不一致'}", flush=True)
    if miss_o:
        print(f"  缺少：{miss_o}", flush=True)
    if extra_o:
        print(f"  多出：{extra_o}", flush=True)
    print(f"app.tgz 成员集合：{'一致' if not (miss_i or extra_i) else '不一致'}", flush=True)
    if miss_i:
        print(f"  缺少：{miss_i}", flush=True)
    if extra_i:
        print(f"  多出：{extra_i}", flush=True)
    print(f"成员顺序：{'与原 fpk 完全一致' if order_ok else '与原 fpk 不同（不影响安装）'}", flush=True)

    print_structure(fpk_bytes)

    if errors:
        print("\n[自检失败]", file=sys.stderr, flush=True)
        for e in errors:
            print(f"  - {e}", file=sys.stderr, flush=True)
        sys.exit(1)

    print(f"\n[自检通过] app.tgz 内含 {APP_NAME}（ELF64 x86-64，> 1MiB），结构校验通过。", flush=True)


# ---------------------------------------------------------------------------
# 勘察模式
# ---------------------------------------------------------------------------


def inspect_fpk(path: Path) -> None:
    step(f"勘察 {path}")
    data = path.read_bytes()
    print(f"文件大小：{human(len(data))}", flush=True)
    print(f"gzip 头：{data[:10].hex()} (FLG=0x{data[3]:02x} mtime={struct.unpack_from('<I', data, 4)[0]} "
          f"XFL=0x{data[8]:02x} OS=0x{data[9]:02x})", flush=True)
    raw = gzip.decompress(data)
    fmt = "ustar/posix" if raw[257:263] == b"ustar\x00" else repr(raw[257:265])
    tail = len(raw) - len(raw.rstrip(b"\0"))
    print(f"tar 格式：{fmt}，末尾零块 {tail} 字节", flush=True)

    print("\n--- 外层 ---", flush=True)
    print(f"{'成员名':<48} {'类型':<6} {'模式':<7} {'uid':<4} {'gid':<4} {'mtime':<12} {'大小':>10}", flush=True)
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as t:
        for m in t.getmembers():
            kind = "dir" if m.isdir() else ("file" if m.isfile() else m.type.decode("latin1"))
            print(f"{m.name:<48} {kind:<6} {oct(m.mode):<7} {m.uid:<4} {m.gid:<4} {m.mtime:<12} {m.size:>10}", flush=True)
        app_tgz = t.extractfile(APP_TGZ_NAME).read()

    print(f"\n--- {APP_TGZ_NAME}（md5={hashlib.md5(app_tgz).hexdigest()}）---", flush=True)
    print(f"{'成员名':<48} {'类型':<6} {'模式':<7} {'uid':<4} {'gid':<4} {'mtime':<12} {'大小':>10}", flush=True)
    for m in list_members(app_tgz):
        kind = "dir" if m.isdir() else ("file" if m.isfile() else m.type.decode("latin1"))
        print(f"{m.name:<48} {kind:<6} {oct(m.mode):<7} {m.uid:<4} {m.gid:<4} {m.mtime:<12} {m.size:>10}", flush=True)


# ---------------------------------------------------------------------------
# main
# ---------------------------------------------------------------------------


def main() -> None:
    ap = argparse.ArgumentParser(
        description="fnOS (飞牛 NAS) FPK 打包脚本（纯 Python 标准库，无第三方依赖）",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("--version", help="写入 manifest 的 version（默认沿用 fpk-package/manifest 中的值）")
    ap.add_argument("--skip-frontend", action="store_true", help="跳过前端构建（复用已有 backend/dist）")
    ap.add_argument("--output", default=str(DEFAULT_OUTPUT), help=f"输出 fpk 路径（默认 {DEFAULT_OUTPUT}）")
    ap.add_argument("--go", default=None, help="go 可执行文件路径（默认自动探测）")
    ap.add_argument("--legacy-modes", action="store_true",
                    help="使用与原 fpk 完全一致的老权限位（文件 0666 / 目录 0777），默认用规范 0644/0755")
    ap.add_argument("--inspect", metavar="FPK", help="仅勘察指定 fpk 的结构，不做打包")
    args = ap.parse_args()

    if args.inspect:
        p = Path(args.inspect)
        if not p.is_file():
            die(f"勘察目标不存在：{p}")
        inspect_fpk(p if p.is_absolute() else (ROOT / p))
        return

    print("fnOS FPK 打包脚本（零依赖 Python 实现）", flush=True)
    print(f"仓库根目录：{ROOT}", flush=True)

    out_path = Path(args.output)
    if not out_path.is_absolute():
        out_path = (ROOT / out_path).resolve()

    mtime = int(os.environ.get("SOURCE_DATE_EPOCH", "0")) or int(time.time())

    if not args.skip_frontend:
        run_frontend_build()
    else:
        step("1/5 跳过前端构建（--skip-frontend）")

    go_exe = resolve_go(args.go)
    info(f"使用 Go：{go_exe}")
    binary = APP_DIR / APP_NAME
    run_go_build(go_exe, binary)

    check_required(args.skip_frontend, binary)

    app_tgz = build_app_tgz(mtime, args.legacy_modes)
    fpk = build_fpk_bytes(app_tgz, mtime, args.legacy_modes, args.version)
    step("4/5 写出 fpk")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_bytes(fpk)
    info(f"已生成：{out_path}  {human(len(fpk))}")

    self_check(out_path, args.legacy_modes)

    print(f"\n[完成] 打包成功：{out_path}", flush=True)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        die("用户中断。")
