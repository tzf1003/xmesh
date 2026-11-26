# Codex 委托运行器冒烟验证

2026-11-26 在 Linux x86_64 上使用 Codex CLI 0.172.0，验证 [#208](https://github.com/tzf1003/xmesh/issues/208) 和 [CTR-DLG-05](../contracts/delegate.md#ctr-dlg-05) 中的内置命令。CLI 在临时工作目录中运行，会话保存在隔离的 `CODEX_HOME`。提示词只要求回复测试标记。

## 参数核对

`codex exec --help` 列出了 `--sandbox read-only` 和 `--sandbox workspace-write`。`codex exec resume --help` 给出的用法是 `codex exec resume [OPTIONS] [SESSION_ID] [PROMPT]`，其中 `PROMPT` 为 `-` 时从标准输入读取。旧命令把 `--sandbox` 放在 `resume` 后，真实 CLI 返回 `error: unexpected argument '--sandbox' found`，退出码为 `2`。把该参数放在 `exec` 和 `resume` 之间后，CLI 接受两个模式。隔离会话的 `turn_context` 也记录了对应的 `read-only` 和 `workspace-write`。

## 真实 CLI

运行器的读模式命令以标准输入传入提示词。在首轮结束后，从 `thread.started` 的 `thread_id` 取出 `$session_id`。

```sh
printf '%s' 'Use no tools. Reply exactly XM208_OK. For the next turn remember the token XM208_RECALL.' |
  codex exec --json --skip-git-repo-check --sandbox read-only -

printf '%s' 'Use no tools. Reply exactly with the token I asked you to remember in the first turn.' |
  codex exec --sandbox read-only resume "$session_id" --json --skip-git-repo-check -
```

两条命令均退出 `0`，发出 `turn.completed`。两次 `thread.started` 给出相同的 `thread_id`。两轮最后的 `item.completed` 都是 `agent_message`，文本依次为 `XM208_OK` 和 `XM208_RECALL`。续聊仅要求复述首轮标记，收到 `XM208_RECALL` 验证了会话上下文被保留。

## 委托运行器

通过 `internal/delegate` 的测试 harness 调用 `delegate.run`。测试将 `cmd` 指向一个转发脚本，设置隔离的 `CODEX_HOME` 后将参数原样传给真实 CLI。写模式在测试配置中启用了 `allow_write`。首次尝试在 40 秒内超时。为复测新建隔离的 `CODEX_HOME`，并把任务超时设为 95 秒，四次调用在约 40 秒内全部完成。

| 模式 | 新任务 | 按 `session_id` 续聊 | 两轮生效的沙箱 |
| --- | --- | --- | --- |
| 读模式 | `done`，退出 `0`，返回 `XM208_OK` | `done`，退出 `0`，返回 `XM208_RECALL`，会话 ID 与首轮一致 | `read-only` |
| 写模式 | `done`，退出 `0`，返回 `XM208_OK` | `done`，退出 `0`，返回 `XM208_RECALL`，会话 ID 与首轮一致 | `workspace-write` |

沙箱模式取自隔离会话的 `turn_context`。验证范围为 CLI 参数、续聊上下文、xmesh 的会话 ID 和最终答复解析。
