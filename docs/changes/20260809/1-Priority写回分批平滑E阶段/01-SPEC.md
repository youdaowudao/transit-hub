# Priority 写回分批平滑 E 阶段需求快照

## 人类希望解决的目标

原本每轮 Priority 写回会把本轮所有需要修改的目标集中处理。用户只希望把这一轮待写目标按 2、3、4、5 个秒点拆开，顺序写完，降低主站瞬时压力，同时保留已经正确的健康档位、全局排序和 `10000` / `100000` 收敛修复。不再继续把这件事扩展成 D 阶段那样的预算、公平和全站目标级调度系统。

## 已确认的 Git 基线

- E 使用临时本地分支 `dev-e`，从 `5446262` 创建独立 worktree。该提交已经包含 A、B、C 和 `ced2a1a` 安全闸门，不包含 `79b384c` 的 D 阶段。
- 当前 `dev` 的 `f69dccf` 和全部未提交 D 修补保持原样，只作为对照，不在 E 开发期间回退、清理或继续修改。
- 当前本地引用显示 `origin/dev=5446262`，`dev` 只在本地领先 `79b384c`、`f69dccf` 两个提交；没有本地远端跟踪分支包含 `79b384c`。按用户决定，E 阶段不执行 `fetch`、`ls-remote`、`push`、PR 或其他远端操作。
- `79b384c` 同时混合了 Priority 收敛修复和 D 压力控制，不能整体 cherry-pick 到 `dev-e`。只能按行为合同抽取必要代码和测试。
- `f69dccf` 只修改无关的 `UpstreamView.vue`。最终提交 E 前按原提交顺序移植到 `dev-e`，不得因此把 `79b384c` 一起带入。
- `dev-e` 是临时本地分支，不直接推送远端。本阶段只完成本地干净线路和固定 `dev` 替换；远端发布以后作为单独任务重新确认。

## 必须承接的既有行为

- 保留 A：健康档位、workspace 全局排序、同一 target 跨分组去重。
- 保留 B：`PendingSignature`、最新计划覆盖、`minWriteInterval=30s`、workspace 级 `AcquirePrioritySyncLease`、人工 Priority 漂移默认只告警。
- 保留 C：`probe/reconcile/writeback` 三条独立循环、inventory snapshot 和代次取消；探活不直接触发主站写回。
- 保留安全闸门：账号级 `MutationCoordinator`、repository-backed mutation lease、generation、人工/自动写互斥、authoritative read 和 readback。
- 从 `79b384c` 及当前修补中只抽取经过定向测试和独立 review 证明必要的 Priority 收敛行为：唯一 `desiredPriorityByTarget`、`desired-priority-v1` 签名、跨档即时 pending、`10000` / `100000` 恢复、normal/incident 所有权隔离，以及正确的 checkpoint 与 snapshot 失效顺序。

## E 的固定行为

- E 只改“正常 Priority 写回的出站节奏”，不修改健康状态机和安全写合同。
- 拆分范围固定为单个 workspace；每轮统计该 workspace 去重后、主站实际 Priority 与本轮期望 Priority 不一致的 target，总数记为 `T`。
- 一轮拆分单位是“一个 target 的正常 Priority mutation”，不是 group，也不是原始 HTTP 请求数。
- 新增配置 `writebackSpreadSeconds`，中文名称“写回分批秒数”，默认 `1`，允许范围 `1-10`。
- 配置值 `N` 表示把本轮 `T` 个 target 分成最多 `N` 个执行槽；`batchSize = ceil(T/N)`，相邻执行槽最早间隔 `1s`。
- 每个执行槽内部继续顺序处理，不新增 target 并发。前一槽未完成时，后一槽不得重叠执行，因此 `N` 是展开槽数，不是保证 `N` 秒内完成的 SLA。
- 同一轮先处理“主站实际档位与期望档位不同”的 target，再处理“仅同档顺序微调”的 target；每层内部沿用稳定目标顺序。
- 新一轮计划或新签名出现时，尚未发出的旧执行槽直接作废，只保留最新计划；不建立跨轮次持久 batch cursor。
- 单个 target 写失败时不记作已应用，也不在同一执行槽内立即重试；后续 target 继续，失败目标等待最新计划的后续轮次。
- 页面显示当前 workspace 的 `pendingTargetCount` 和 `writebackSpreadSeconds`。`pendingTargetCount` 使用新的明确字段，不能复用 D 的 `QueueLength`。

## 必须删除或不得移植的 D 行为

- 不移植 `DEnabled`、workspace read/write budget、`queueLimit`、`serviceTurnUsed`、D failure backoff、D fairness cursor 和 D 观测字段。
- 不移植 `priority_pressure_control.go`、D 压力测试、D 设置项和 D 专用迁移语义。
- 不保留 `TryAcquireNormalPriorityWriteLease` 与 `TryReserveNormalPriorityWrite` 组成的全站 `1 target/s` 门控。
- 保留 B 的 workspace lease 和安全闸门的账号 mutation lease；这两层分别负责 workspace 串行和同账号人工/自动写安全，不能随 D 一起删除。
- E 不建立跨 workspace 的全站共享队列。不同 workspace 继续按各自的 B/C 计划和 lease 工作。

## 成功标准

1. `dev-e` 的历史不包含 `79b384c`，源码和迁移中不存在 D 的有效路径。
2. `spreadSeconds=1` 时，一轮正常 Priority 写回与 D 之前的单轮顺序处理等价，不增加等待和总写次数。
3. `spreadSeconds=N` 时，本轮 `T` 个 target 按 `ceil(T/N)` 切成最多 `N` 个不重叠执行槽，旧计划不会跨轮次回放。
4. 页面显示去重后的 `pendingTargetCount`，用户能直接判断本轮是几十个还是几百个目标。
5. `10000+`、`100000` 恢复和其他跨档变化继续正常收敛；同档微调继续遵守 B 的 30 秒合并。
6. 多实例下同一 workspace 仍受 B lease 串行，同一账号人工/自动写仍受安全 mutation lease 和 generation 保护。
7. 最终本地 `dev` 线路不包含 `79b384c`，但保留 `f69dccf` 的等价无关功能和 E 的新提交；`origin/dev` 在本阶段保持 `5446262` 不变。

## 明确不做

- 不在当前 `dev` 和未提交 D 修补上继续实现 E。
- 不通过 `DEnabled=false` 假装删除 D；E 的有效调用链中不得存在 D global gate。
- 不修改探活周期、健康档位区间、全局比较器或去重逻辑。
- 不重新设计安全闸门，不撤销 manual/automatic mutation fencing。
- 不在 E 加入“长期延迟 + 至少保留 N 个健康目标时自动禁用”的新规则。
- 不把当前单 target authoritative read/readback 成本优化塞进 E；本阶段只改变一轮中多个 target 的时间分布。

## 固定风险

- 如果主要压力来自单个 target 的 authoritative read/readback，而不是一轮 target 集中写回，E 只能缓解突发，不能消除单 target 固有成本。
- 从 D 抽取收敛修复时最容易误带 D 状态和门控；实现必须以 `5446262` 为底，逐项搬行为和测试，禁止按文件整包复制。
- 最终不能把旧 `dev` 普通 merge 到 `dev-e`，否则 `79b384c` 会重新进入历史。集成必须按计划重新建立干净 `dev` 线路。
- “全部清除旧 D”固定指：`79b384c` 不再被本地分支/worktree 引用，当前 tracked D 修补全部丢弃，D 专用 untracked 文件按精确路径删除，不保留 stash 或补丁。Git reflog 可能暂时保存不可达对象；本阶段不执行危险的 reflog expire 或强制 garbage collection。
