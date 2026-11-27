# 在飞工作快照（HANDOFF.md）

规则见 [AGENTS.md](AGENTS.md)。本文件只记录**正在进行**的工作，结束后删掉对应行。与 issue 冲突时以 issue 为准。

快照时间：2026-11-26，基线 `master` 在 `1b748e9`（#199）。

## 在飞

| issue | 执行方 | 分支 | 状态 | 卡在哪 / 下一步 |
| --- | --- | --- | --- | --- |
| #219 加入 AGENTS.md 与 HANDOFF.md | claude | `claude/project-thread-tse5ie` | 进行中，PR 待评审 | 等另一个会话交叉评审和 CI 全绿后合并；看板需用户先建，见下 |
| #215 委托：活会话回复回收 | antigravity | `antigravity/delegate-session-reply` | 待评审，已开草稿 PR | 等 CI 全绿与另一个会话交叉评审 |

## 需要用户做的事

- 新建 GitHub Project 看板并开启自动收录，步骤见 [AGENTS.md](AGENTS.md) 第 7 节。仓库的 token 在会话里不能调用 GraphQL，所以这一步只能由用户在账号上完成。
- #212 等用户确认两项委托安全默认值。
- 带 `发版前冒烟` 标签的 issue 大多需要用户的 Windows、Mac 或其他真机。

## 待办概览

- 待认领的 issue：见 `状态/待办` 标签筛选。第一批由梳理文档与代码得来，来源在每张 issue 的「来源」一节。
- 发版前冒烟项：`发版前冒烟` 标签筛选。

## 需要确认的遗留分支

仓库里有 110 多个旧的 `claude/*` 分支，其中不少领先 `master` 的提交其实已经以压缩合并的形式进了 `master`，仅凭提交数无法区分。梳理时没有删除或改动任何分支。下面三个最近的还显示领先，需要它们的作者或用户确认是否已被合并取代：

- `claude/project-thread-xh88rj`（领先 1 个提交，标题与 #199 相同，疑似已被 #199 取代）
- `claude/project-thread-6ksusx`（领先 9 个提交，11-17，`ci(ui)` 签名相关，疑似已被 #198 吸收）
- `claude/project-thread-59hi6m`（领先 12 个提交，11-16，委托测试，疑似已被 #195 吸收）

确认前不要删除，也不要往这些分支推送。
