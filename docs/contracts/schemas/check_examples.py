#!/usr/bin/env python3
# 校验契约模式与文档库链接。
# 正例必须通过对应的 JSON Schema，反例必须失败。
# 另外扫描 docs 下的 Markdown 相对链接，避免目录改名后索引悬空。

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

try:
    from jsonschema import Draft202012Validator, FormatChecker
except ImportError:
    print(
        "缺少 jsonschema。请先执行: python3 -m pip install -r docs/contracts/schemas/requirements.txt",
        file=sys.stderr,
    )
    sys.exit(2)

if "date-time" not in FormatChecker.checkers:
    print("date-time 格式检查器不可用。请安装 rfc3339-validator 后重试。", file=sys.stderr)
    sys.exit(2)

# 仓库根目录：本文件位于 docs/contracts/schemas/
根目录 = Path(__file__).resolve().parents[3]
模式目录 = Path(__file__).resolve().parent
示例目录 = 模式目录 / "examples"
文档目录 = 根目录 / "docs"

# 正例文件名到模式文件名的对应关系。
正例 = {
    "xmesh.config.json": "config.schema.json",
    "xmesh.gateway-pat.json": "config.schema.json",
    "xmesh.ephemeral.json": "config.schema.json",
    "error-resource-busy.json": "error.schema.json",
    "error-pty-limit.json": "error.schema.json",
    "error-invalid-cwd.json": "error.schema.json",
    "error-conflict.json": "error.schema.json",
    "error-policy-denied.json": "error.schema.json",
    "error-exec-saturated.json": "error.schema.json",
    "lease.json": "lease.schema.json",
    "audit-event.json": "audit-event.schema.json",
    "xmesh.ingress-public.json": "config.schema.json",
}

# 反例同样按文件名选择模式。这些文件必须无法通过校验。
反例 = {
    "invalid/error-busy-missing-holder.json": "error.schema.json",
    "invalid/config-relay.json": "config.schema.json",
    "invalid/config-lan-ephemeral.json": "config.schema.json",
    "invalid/config-pat-empty-scopes.json": "config.schema.json",
    "invalid/audit-with-stdout.json": "audit-event.schema.json",
    "invalid/config-funnel-with-domain.json": "config.schema.json",
    "invalid/error-pty-limit-named-active.json": "error.schema.json",
}


def 加载_json(路径: Path) -> object:
    """读取 JSON 文件。失败时打印路径，便于定位哪一份夹具坏了。"""
    print(f"读取 {路径.relative_to(根目录)}")
    return json.loads(路径.read_text(encoding="utf-8"))


def 校验器(模式名: str) -> Draft202012Validator:
    模式 = 加载_json(模式目录 / 模式名)
    Draft202012Validator.check_schema(模式)
    # format_checker 打开 date-time 等格式约束，避免审计时间写成任意字符串也能通过。
    return Draft202012Validator(模式, format_checker=FormatChecker())


def 检查正例() -> int:
    失败 = 0
    for 文件名, 模式名 in 正例.items():
        实例 = 加载_json(示例目录 / 文件名)
        检查 = 校验器(模式名)
        错误 = sorted(检查.iter_errors(实例), key=lambda 项: list(项.path))
        if 错误:
            失败 += 1
            print(f"正例未通过 {文件名} ← {模式名}")
            for 项 in 错误:
                print(f"  路径 /{'/'.join(str(p) for p in 项.path)}: {项.message}")
        else:
            print(f"正例通过 {文件名}")
    return 失败


def 检查反例() -> int:
    失败 = 0
    for 文件名, 模式名 in 反例.items():
        实例 = 加载_json(示例目录 / 文件名)
        检查 = 校验器(模式名)
        错误 = list(检查.iter_errors(实例))
        if not 错误:
            失败 += 1
            print(f"反例竟然通过了 {文件名} ← {模式名}")
        else:
            print(f"反例按预期失败 {文件名}（{len(错误)} 条）")
    return 失败


def 检查链接() -> int:
    """只检查指向相对文件的链接。锚点不在这里解析。"""
    失败 = 0
    链接表达式 = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
    for 文档 in 文档目录.rglob("*.md"):
        文本 = 文档.read_text(encoding="utf-8")
        for 匹配 in 链接表达式.finditer(文本):
            目标 = 匹配.group(1).strip()
            if 目标.startswith(("http://", "https://", "mailto:")):
                continue
            if 目标.startswith("#"):
                continue
            路径部分 = 目标.split("#", 1)[0]
            if not 路径部分:
                continue
            绝对 = (文档.parent / 路径部分).resolve()
            if not 绝对.exists():
                失败 += 1
                print(f"悬空链接 {文档.relative_to(根目录)} -> {目标}")
    if 失败 == 0:
        print("文档相对链接全部可解析")
    return 失败


def main() -> int:
    print("开始校验 xmesh 契约模式与文档链接")
    失败 = 检查正例() + 检查反例() + 检查链接()
    if 失败:
        print(f"校验失败，共 {失败} 项")
        return 1
    print("校验通过")
    return 0


if __name__ == "__main__":
    sys.exit(main())
