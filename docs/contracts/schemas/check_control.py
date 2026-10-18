#!/usr/bin/env python3
# 校验本机控制通道契约（CTR-CTL-*）。
# 由 check_examples.py 调用，也可以单独运行。检查内容：
#   1. 所有 control-*.schema.json 是合法的 draft 2020-12，$ref 全部能解析，定义名全局唯一。
#   2. examples/control/<定义名>.<变体>.json 通过同名定义，invalid/ 下的同名文件必须失败。
#   3. control-routes.json 符合 control-routes.schema.json，引用的定义存在，每个能力至少有一条路由，
#      每个出现在路由里的请求与响应定义至少有一个正例，错误码集合与 x-http-status 一致。

from __future__ import annotations

import json
import sys
from pathlib import Path

try:
    from jsonschema import Draft202012Validator, FormatChecker
    from referencing import Registry, Resource
except ImportError:
    print(
        "缺少 jsonschema 或 referencing。请先执行: python3 -m pip install -r docs/contracts/schemas/requirements.txt",
        file=sys.stderr,
    )
    sys.exit(2)

模式目录 = Path(__file__).resolve().parent
根目录 = 模式目录.parents[2]
示例目录 = 模式目录 / "examples" / "control"
反例目录 = 示例目录 / "invalid"
路由文件 = 模式目录 / "control-routes.json"


def 读取(路径: Path) -> object:
    return json.loads(路径.read_text(encoding="utf-8"))


def 加载模式() -> tuple[Registry, dict[str, tuple[str, dict]], list[str]]:
    """返回注册表、定义名到（模式 $id, 定义）的索引，以及加载期间发现的问题。"""
    问题: list[str] = []
    资源 = []
    索引: dict[str, tuple[str, dict]] = {}
    for 路径 in sorted(模式目录.glob("*.schema.json")):
        内容 = 读取(路径)
        Draft202012Validator.check_schema(内容)
        资源.append((内容["$id"], Resource.from_contents(内容)))
        if not 路径.name.startswith("control-"):
            continue
        for 名称, 定义 in 内容.get("$defs", {}).items():
            if 名称 in 索引:
                问题.append(f"定义名重复 {名称}：{路径.name} 与 {索引[名称][0]}")
            索引[名称] = (内容["$id"], 定义)
    return Registry().with_resources(资源), 索引, 问题


def 收集引用(节点: object, 输出: list[str]) -> None:
    if isinstance(节点, dict):
        for 键, 值 in 节点.items():
            if 键 == "$ref" and isinstance(值, str):
                输出.append(值)
            else:
                收集引用(值, 输出)
    elif isinstance(节点, list):
        for 值 in 节点:
            收集引用(值, 输出)


def 解析引用(注册表: Registry, 基准: str, 引用: str) -> bool:
    try:
        注册表.resolver(base_uri=基准).lookup(引用)
        return True
    except Exception as 错误:  # referencing 的异常类型随版本变化
        print(f"引用无法解析 {基准} -> {引用}: {错误}")
        return False


def 检查引用(注册表: Registry) -> int:
    失败 = 0
    for 路径 in sorted(模式目录.glob("control-*.schema.json")):
        内容 = 读取(路径)
        引用: list[str] = []
        收集引用(内容, 引用)
        for 项 in 引用:
            if not 解析引用(注册表, 内容["$id"], 项):
                失败 += 1
    print("控制模式的 $ref 全部可解析" if 失败 == 0 else f"{失败} 个 $ref 无法解析")
    return 失败


def 定义校验器(注册表: Registry, 模式id: str, 名称: str) -> Draft202012Validator:
    包装 = {"$schema": "https://json-schema.org/draft/2020-12/schema", "$ref": f"{模式id}#/$defs/{名称}"}
    return Draft202012Validator(包装, registry=注册表, format_checker=FormatChecker())


def 检查示例(注册表: Registry, 索引: dict[str, tuple[str, dict]]) -> tuple[int, set[str]]:
    失败 = 0
    已覆盖: set[str] = set()
    for 路径 in sorted(示例目录.glob("*.json")):
        名称 = 路径.name.split(".", 1)[0]
        if 名称 not in 索引:
            print(f"正例对应的定义不存在 {路径.name}")
            失败 += 1
            continue
        错误 = sorted(定义校验器(注册表, 索引[名称][0], 名称).iter_errors(读取(路径)), key=lambda 项: list(项.path))
        if 错误:
            失败 += 1
            print(f"控制正例未通过 {路径.name}")
            for 项 in 错误[:6]:
                print(f"  路径 /{'/'.join(str(p) for p in 项.path)}: {项.message[:200]}")
        else:
            已覆盖.add(名称)
    for 路径 in sorted(反例目录.glob("*.json")):
        名称 = 路径.name.split(".", 1)[0]
        if 名称 not in 索引:
            print(f"反例对应的定义不存在 {路径.name}")
            失败 += 1
            continue
        错误 = list(定义校验器(注册表, 索引[名称][0], 名称).iter_errors(读取(路径)))
        if not 错误:
            失败 += 1
            print(f"控制反例竟然通过了 {路径.name}")
    print(f"控制契约正例 {len(list(示例目录.glob('*.json')))} 个，反例 {len(list(反例目录.glob('*.json')))} 个")
    return 失败, 已覆盖


def 拆引用(引用: str) -> tuple[str, str]:
    模式id, _, 片段 = 引用.partition("#/$defs/")
    return 模式id, 片段


def 检查路由(注册表: Registry, 索引: dict[str, tuple[str, dict]], 已覆盖: set[str]) -> int:
    失败 = 0
    路由数据 = 读取(路由文件)
    错误 = list(定义校验器(注册表, "urn:xmesh:schema:control-routes:1", "ControlRoutesFile").iter_errors(路由数据))
    for 项 in 错误:
        失败 += 1
        print(f"路由表不合格 /{'/'.join(str(p) for p in 项.path)}: {项.message[:200]}")
    if 错误:
        return 失败

    编号: set[str] = set()
    方法路径: set[tuple[str, str]] = set()
    能力: set[str] = set()
    for 路由 in 路由数据["routes"]:
        if 路由["id"] in 编号:
            失败 += 1
            print(f"路由编号重复 {路由['id']}")
        编号.add(路由["id"])
        键 = (路由["method"], 路由["path"])
        if 键 in 方法路径:
            失败 += 1
            print(f"路由重复 {键}")
        方法路径.add(键)
        能力.add(路由["capability"])
        if 路由["stream"] and 路由["response"] is not None:
            失败 += 1
            print(f"事件流路由不应有 response {路由['id']}")
        if 路由["success"] == 204 and 路由["response"] is not None:
            失败 += 1
            print(f"204 路由不应有 response {路由['id']}")
        if 路由["success"] != 204 and not 路由["stream"] and 路由["response"] is None:
            失败 += 1
            print(f"{路由['id']} 缺少 response")
        if 路由["method"] == "GET" and 路由["body"] is not None:
            失败 += 1
            print(f"GET 路由不应有 body {路由['id']}")
        if 路由["if_match"] == "required" and 路由["method"] not in ("PUT", "PATCH"):
            失败 += 1
            print(f"If-Match 只用于 PUT 与 PATCH {路由['id']}")
        for 字段 in ("query", "body", "response"):
            引用 = 路由[字段]
            if 引用 is None:
                continue
            模式id, 名称 = 拆引用(引用)
            if 名称 not in 索引 or 索引[名称][0] != 模式id:
                失败 += 1
                print(f"路由 {路由['id']} 的 {字段} 指向不存在的定义 {引用}")
            elif 名称 not in 已覆盖:
                失败 += 1
                print(f"路由 {路由['id']} 的 {字段} 定义 {名称} 没有正例")

    全部能力 = set(索引_能力(注册表))
    缺失 = 全部能力 - 能力
    多余 = 能力 - 全部能力
    if 缺失 or 多余:
        失败 += 1
        print(f"能力与路由表不一致：没有路由的能力 {sorted(缺失)}，未声明的能力 {sorted(多余)}")

    错误码 = 读取(模式目录 / "control-error.schema.json")
    状态表 = set(错误码["x-http-status"])
    枚举 = set(错误码["$defs"]["ControlErrorCode"]["enum"])
    if 状态表 != 枚举:
        失败 += 1
        print(f"x-http-status 与 ControlErrorCode 不一致：{sorted(状态表 ^ 枚举)}")
    print("控制路由表一致" if 失败 == 0 else f"路由表有 {失败} 项问题")
    return 失败


def 索引_能力(注册表: Registry) -> list[str]:
    公共 = 读取(模式目录 / "control-common.schema.json")
    return 公共["$defs"]["Capability"]["enum"]


def main() -> int:
    print("开始校验本机控制通道契约")
    注册表, 索引, 问题 = 加载模式()
    for 项 in 问题:
        print(项)
    失败 = len(问题) + 检查引用(注册表)
    if 失败:
        return 失败
    示例失败, 已覆盖 = 检查示例(注册表, 索引)
    return 失败 + 示例失败 + 检查路由(注册表, 索引, 已覆盖)


if __name__ == "__main__":
    sys.exit(1 if main() else 0)
