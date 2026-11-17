#!/usr/bin/env python3
"""收集依赖的许可证，生成 THIRD-PARTY-LICENSES.md。

为什么要它：本项目 MIT，但二进制里链着 BSD-3 / Apache-2.0 等第三方代码，那些许可证要求
分发二进制时保留版权声明。发版前跑一次，把生成的文件提交进仓库。

用法：python3 tools/thirdparty.py [输出文件]      默认 THIRD-PARTY-LICENSES.md
"""

import subprocess
import sys
from pathlib import Path

CANDIDATES = ("LICENSE", "LICENSE.md", "LICENSE.txt", "LICENSE-MIT", "COPYING", "COPYING.txt", "NOTICE")


def compiled_modules() -> list[dict]:
    """只取「真的编进二进制」的模块。

    不能用 `go list -m all`：那是整个模块图，含一大堆没被编译的（`Dir` 是空的，或者
    replace 指向本地 "."），会误报「找不到许可证」。`go list -deps -f` 直接给出每个
    被 import 的包所属模块及目录，顺手还能拿去重。
    """
    out = subprocess.run(
        ["go", "list", "-deps", "-f", "{{with .Module}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}", "./..."],
        capture_output=True, text=True, check=True,
    ).stdout
    seen: dict[tuple[str, str], dict] = {}
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) != 3 or not parts[0]:
            continue
        path, version, directory = parts[0], parts[1] or "(devel)", parts[2]
        if (path, version) in seen:
            continue
        seen[(path, version)] = {"path": path, "version": version, "dir": directory}
    return [seen[k] for k in sorted(seen)]


def license_file(directory: Path) -> Path | None:
    if not directory.is_dir():
        return None
    for name in CANDIDATES:
        p = directory / name
        if p.is_file():
            return p
    for p in sorted(directory.iterdir()):
        if p.is_file() and p.name.lower().startswith(("license", "copying")):
            return p
    return None


def main() -> int:
    out_path = Path(sys.argv[1] if len(sys.argv) > 1 else "THIRD-PARTY-LICENSES.md")
    main_mod = subprocess.run(["go", "list", "-m"], capture_output=True, text=True, check=True).stdout.strip()
    mods = compiled_modules()

    rows, body, missing = [], [], []
    for m in mods:
        if m["path"] == main_mod:
            continue  # 本项目自己（许可证见 LICENSE）
        path, version = m["path"], m["version"]
        directory = Path(m["dir"])
        lic = license_file(directory)
        if lic is None:
            rows.append(f"| `{path}` | {version} | ⚠️ 未找到（{directory or '未下载'}） |")
            missing.append(path)
            continue
        rows.append(f"| `{path}` | {version} | `{lic.name}` |")
        text = lic.read_text(errors="replace").strip()
        body.append(f"## {path} {version}\n\n```\n{text}\n```\n")

    header = (
        "# 第三方许可证\n\n"
        "> 本文件由 `tools/thirdparty.py` 生成（依赖清单来自 `go list -deps`，只含真的编进二进制的模块）。\n"
        "> clipnest 自身以 MIT 许可发布，见 `LICENSE`。\n\n"
        f"二进制里链接了下列 {len(rows)} 个模块；每个模块的许可证原文附在其后。\n\n"
        "| 模块 | 版本 | 许可证文件 |\n| --- | --- | --- |\n"
    )
    out_path.write_text(header + "\n".join(rows) + "\n\n" + "\n".join(body))

    print(f"已写 {out_path}：{len(rows)} 个模块，其中 {len(missing)} 个没找到许可证文件")
    if missing:
        print("⚠️ 有模块缺许可证，发版前先人工确认：" + ", ".join(missing))
    return 0


if __name__ == "__main__":
    sys.exit(main())
