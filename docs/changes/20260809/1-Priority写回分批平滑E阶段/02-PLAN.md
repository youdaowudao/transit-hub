# Priority 写回分批平滑 E 阶段实施计划

## 一、分支与 worktree

1. 当前 `/home/ss/data/projects/transit-hub` 保持在 `dev`，保留 `f69dccf`、`79b384c` 和全部未提交 D 修补，只作只读对照。
2. 在 `/tmp` 创建独立 worktree 和临时本地分支 `dev-e`，起点固定为 `5446262`。
3. 创建命令固定为 `git worktree add -b dev-e /tmp/transit-hub-dev-e 5446262`；执行前必须确认该目录不存在、`dev-e` 分支不存在、`5446262` 可解析。
4. `dev-e` 不直接推送远端，不创建其他远端分支。E 阶段禁止 `fetch`、`pull`、`ls-remote`、`push` 和 PR 操作。
5. 当前本地 `origin/dev` 固定指向 `5446262`，`dev` 本地领先两个提交；本阶段只依赖这些本地引用，不声称已联网复核远端。
6. 所有 commit 和分支移动仍需用户许可。
7. E 实现完成前不处理当前 `dev` 的脏工作区；不得 stash、丢弃、回退或覆盖这些用户已有改动。

## 二、先建立保留与删除清单

### 保留

- A 的档位、全局比较器和 target 去重。
- B 的 workspace 签名/pending、30 秒门控、`AcquirePrioritySyncLease` 和人工漂移告警。
- C 的三条循环、snapshot、generation context 和 reconcile/writeback 分离。
- 安全闸门的账号级 `MutationCoordinator`、repository mutation lease、generation、authoritative read、readback 和人工写保护。

### 从 D 混合提交中抽取

- `desiredPriorityByTarget` 作为签名和写回的唯一期望值来源。
- `desired-priority-v1` 签名。
- 跨档 pending 不被旧相对顺序签名吞掉。
- `10000` / `100000` 恢复、normal/incident pending 隔离。
- 已经由定向测试证明必要的 target/workspace checkpoint、context 持久化和 snapshot 失效顺序修复。

### 删除或不移植

- D 专用 migration、`DEnabled`、读写预算、`QueueLimit`、`serviceTurnUsed`、全站 reservation、D fairness/queue/backoff 和相应前端设置/观测。
- `TryAcquireNormalPriorityWriteLease` 与 `TryReserveNormalPriorityWrite` 的全站 `1 target/s` 调用链。
- `QueueLength` 不作为 E 的总数来源。

抽取以行为和测试为单位，不以整个文件为单位；每搬一项先证明其不依赖 D 状态。

## 三、移植无关的当前提交

- `f69dccf` 只修改 `frontend/src/modules/admin/views/UpstreamView.vue`，与 E 无依赖。
- 执行前先用 `git show --stat --oneline f69dccf` 和 `git show --name-only --format= f69dccf` 再次确认只有该文件。
- 获得 Git 操作许可后，在 E 开发开始前执行 `git -C /tmp/transit-hub-dev-e cherry-pick f69dccf`，使提交顺序保持“现有 `V2.1.2` 功能在前，E 新版本在后”。
- cherry-pick 只允许带入该文件的原改动，出现额外路径或冲突时立即停止。
- 完成后用 `git -C /tmp/transit-hub-dev-e log --oneline 5446262..HEAD` 核对只出现 `f69dccf` 的等价移植提交，不得出现 `79b384c`。

## 四、E 的最小实现

1. 在 `5446262` 的 Priority plan 阶段移植唯一 `desiredPriorityByTarget` 和版本化签名，让实际写回只消费该 plan。
2. 新增 `writebackSpreadSeconds`，默认 `1`、范围 `1-10`。D 的 `000023` 不进入干净线路；如 E 需要 schema 变更，可在干净线路复用下一个可用迁移号，但迁移内容只包含 E 字段。
3. 新增语义独立的 `pendingTargetCount`，记录当前 workspace 去重后的实际待写目标数；后端/API/前端统一使用该字段，不复用 `QueueLength`。
4. 复用现有每秒 `runPriorityWritebackLoop` 推进执行槽，不增加按 batch 常驻 goroutine，也不通过 `sleep` 持有 workspace lease。
5. Service 内存只保存当前 `workspace + plan signature` 的轻量 batch plan：初始 `T`、`N`、稳定目标序列和下一个执行槽。它不是持久队列。
6. 每次 writeback tick 最多推进该 workspace 一个执行槽；槽内按现有安全写入口顺序处理最多 `ceil(T/N)` 个 target。
7. 前一执行槽仍在运行时不启动后一槽；计划变化、snapshot generation 变化或人工 generation 失效时丢弃未发槽并按最新计划重建。
8. 进程重启后不恢复旧 cursor；重新 authoritative reconcile，只对当前仍不一致的 target 建新计划。
9. 单 target 失败只保留最新 pending，不在同一槽立即重试，不阻塞本槽后续 target，也不能把 workspace 标记为全部 applied。

## 五、三层串行边界

1. B 的 `AcquirePrioritySyncLease` 保留：防止同一 workspace 被两个实例同时执行计划。
2. 安全闸门的账号 mutation lease 与 generation 保留：防止同一账号人工/自动或多实例写互相覆盖。
3. D 的全站 normal write lease/reservation 删除：它只是全站 `1 target/s` 压力门，不承担前两层已经覆盖的安全职责。

E 不用 `DEnabled=false` 做兼容开关；D 代码在 `dev-e` 基线中本来就不存在。

## 六、前端与可观测

- 在现有 Priority 设置区增加“写回分批秒数”，沿用现有输入样式，不新建页面。
- 在现有连接健康 Priority 状态区域显示“本轮待写 target：N”。
- 至少显示当前 `writebackSpreadSeconds`、`pendingTargetCount`、最后一次执行结果和失败原因。
- 不显示 D 的预算、QueueLimit、global rate hit 或 D 队列指标。

## 七、验证与独立 review

1. 先验证 A/B/C 和安全闸门保留合同，再验证 E；不能只测分批结果。
2. 用 `T=30, N=1/2/3/5` 的 fake clock 用例验证切分、无重叠、最新计划覆盖和失败继续。
3. 重新覆盖 `10000` / `100000`、跨档、同档、人工漂移、incident pending、checkpoint 失败和 snapshot generation。
4. 记录一次 `spreadSeconds=3` 下真实 fake upstream HTTP 调用时间线，分别统计 inventory read、每 target 前读、写入和 readback，证明 E 改变了突发分布且没有增加总 mutation 数。
5. 完成后交给独立 reviewer 对照 `5446262`、`79b384c` 和 E 最终 diff，确认没有 D 残留或 B/C 退化。

## 八、提交与本地 dev 替换

1. E 完成 review 后先报告 `dev-e` 的提交路径、排除路径、版本号和验证结果，等待用户许可后才能提交。
2. E 提交必须使用当时下一个本地版本，并同步代码内置版本；`dev-e` 临时分支名不进入版本或提交标题。
3. 最终不能把旧 `dev` merge 到 `dev-e`。旧 `dev` 含 `79b384c`，普通 merge 会把 D 带回来。
4. 用户已经确认 E 完成后永久丢弃旧 D。执行清理前仍必须重新列出 `git status --short`，确认所有 tracked/untracked 变化都属于旧 D；出现任何新路径或无法归属的变化立即停止。
5. 在当前主 worktree 保持文件不变的前提下，用 `git switch --detach f69dccf` 先解除 `dev` 分支占用。该命令必须在仍指向 `f69dccf` 时执行，不允许替换成其他 commit。
6. 在 `dev-e` 已提交且 worktree 干净后记录准确的 `E_SHA`，再执行 `git branch -f dev <E_SHA>`，让本地固定 `dev` 指向干净线路。该线路必须是 `5446262 -> f69dccf 等价移植提交 -> E 提交`，不包含 `79b384c`。
7. 回到当前主 worktree，用 `git switch -f dev` 丢弃全部 tracked D 修补并切到新的干净 `dev`。禁止使用 `git reset --hard`。
8. 对 untracked 文件不使用宽泛的 `git clean -fd`。先重新核对路径，只允许用 `git clean -f -- <逐个确认的 D 专用路径>` 删除；当前已知候选仅为：
   - `backend/internal/database/migrations/runner_priority_pressure_integration_test.go`
   - `backend/internal/modules/connection_health/priority_reservation_postgres_test.go`
9. 清理后必须同时证明：`git status --short` 为空、`git merge-base --is-ancestor 79b384c dev` 返回非零、`git branch --contains 79b384c` 不再显示任何活动本地分支、`dev` 与 `dev-e` 指向同一干净线路。
10. 然后执行 `git worktree remove /tmp/transit-hub-dev-e`，确认 worktree 已移除后执行 `git branch -d dev-e`。固定 `dev` 保留。
11. 不创建 D 归档 branch、tag、stash 或 patch；旧 D 只允许在 Git reflog 中按 Git 默认保留期暂时不可达，不运行 `git reflog expire` 或 `git gc --prune=now`。
12. 本阶段到此结束。`origin/dev` 保持本地记录中的 `5446262`，不执行任何远端命令；未来发布单独立项和授权。

## 运行影响与回退

- `writebackSpreadSeconds=1` 是运行时第一回退手段，恢复单轮顺序处理。
- `N>1` 只增加最早启动间隔，不承诺在 `N` 秒内完成；慢 target 会顺延后续执行槽，不会触发并发补偿。
- 如果实际压力主要来自单 target authoritative read/readback，应停止扩大 E，另立后续阶段处理。
- E 尚未合入固定 `dev` 前，直接放弃临时 `dev-e` 即可回到当前对照状态；不得为了回退 E 去改写当前 D 工作区。
- 固定 `dev` 完成本地替换后，旧 D 不再作为可切换分支保留；如需回看，只能使用已知 commit hash 或 reflog，不得把 D 重新合回 E。
