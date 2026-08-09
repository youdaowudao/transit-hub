# Priority 写回分批平滑 E 阶段验证口径

## 当前状态

E 已在本地临时 worktree `/tmp/transit-hub-dev-e` 完成实现、独立 review 和提交前复核；提交候选版本为 `V2.1.3`。当前未暂存、未提交、未替换固定 `dev`、未执行远端 Git 操作、未切换固定开发服务。

结论分为两部分：

- E 的分批、收敛、checkpoint、system-internal manual fencing 和 D 清除范围通过定向验证与独立 review。
- 不能宣称“外部主站人工 Priority 永不会被自动写覆盖”：上游没有 revision/ETag/`If-Match` compare-and-set，最后一次权威 GET 与 PUT/POST 之间仍有不可由本仓库消除的窗口。该风险在提交前必须保留为公开已知限制。

## 一、分支与历史验证

1. 当前 `dev-e` 为 `e4e26e3`，其父为 `5446262`；`e4e26e3` 是 `f69dccf` 的等价本地移植提交，只含 `UpstreamView.vue` 的既有无关改动。
2. `git merge-base --is-ancestor 79b384c HEAD` 返回非零；`b548344` 和 `ced2a1a` 均为 `dev-e` 祖先。
3. 固定 `dev` 尚未替换，仍保持为只读对照；替换只允许按 `02-PLAN.md` 第八节在获得用户第二次 Git 许可后执行。
4. 本阶段没有执行 `fetch`、`pull`、`ls-remote`、`push`、PR、commit、branch move 或 worktree remove；本地记录的 `origin/dev` 仍为 `5446262`。

## 二、D 清除验证

对 `backend/` 与 `frontend/` 运行路径搜索，未发现有效的：

- `DEnabled`
- workspace read/write budget
- `QueueLimit` / `QueueLength`
- `serviceTurnUsed`
- `TryAcquireNormalPriorityWriteLease`
- `TryReserveNormalPriorityWrite`
- D global rate、fairness、reservation 和 pressure-control 设置/指标

允许旧 D 文档继续归档，但不能参与运行代码和配置。当前 E worktree 也没有 D 专用 pressure-control 源码或 migration。

## 三、A/B/C 与安全合同回归

本项目唯一主站平台是 Sub2API；本节及 E 的主站 Priority 验证只使用 Sub2API。NewAPI 仅属于外部上游站点，不进入本阶段主站写回范围，也不能代替 Sub2API 测试。

1. A 的档位、全局 target 排序和跨分组去重保持不变。
2. B 的最新 pending、30 秒同档合并、workspace lease 和人工漂移告警保持不变。
3. C 的 probe/reconcile/writeback 分离、snapshot generation 和读写频率保持不变。
4. 同一账号多实例自动写、人工写和旧 generation 仍由 repository-backed mutation lease/fencing 阻止并发覆盖。
5. 正常 Priority 写仍执行 authoritative read 和 readback；失败不能被标记为 applied。
6. Sub2API 的最终 generation 校验位于真实 `bulk-update` POST 之前；校验失败时不会发送 POST。

## 四、Priority 收敛回归

1. `10000+ -> 10-99`、`100000 -> 10-99`、健康到降级、降级到恢复和 suspended/disabled 均能形成正确 pending。
2. 相对顺序不变但 `desiredPriority` 改变时，版本化签名必须改变。
3. 纯延迟数值变化且 `desiredPriority` 未变时保持零写。
4. incident pending 不被 normal 消费；incident 清除后同一 normal 计划能继续收敛。
5. checkpoint 或 workspace 状态保存失败时保持 pending，不产生永久人工漂移冲突。
6. Sub2API fresh 值即使恰好等于新的 desired，也必须先通过 expected 旧值校验；发现外部变化时零写入、记录 conflict，不把该值登记为系统 applied。

## 五、E 分批行为

1. `spreadSeconds=1` 时，`T` 个 target 在一个执行槽内顺序处理，不额外等待、不增加总 mutation 数。
2. `spreadSeconds=3`、`T=30` 时，稳定计划切成 `10/10/10`；三个执行槽最早间隔 `1s`，不能并发重叠。
3. `T < N` 时跳过空槽；`T=0` 时不创建 batch plan。
4. 跨档 target 排在同档微调之前，每层内部顺序稳定。
5. 新签名出现时丢弃旧未发槽；进程重启后从 authoritative 当前值重新计算，不回放旧 cursor。
6. 单 target 失败不阻塞本槽后续 target，不在同槽立即重试，也不把 workspace 标记为全部 applied。
7. 不同 workspace 不使用 D 的全站队列；同一 workspace 仍由 B lease 串行。
8. `T=7,N=5` 切为 `2/2/2/1`，`T=0` 不保留空 batch；失败 target 不重试同槽、后续 target 继续，`pendingTargetCount` 计回失败 target。
9. 中间槽所在实例退出后，另一实例没有本地 cursor 时立即 authoritative reconcile，不等待旧的 30 秒 deadline。

## 六、字段与页面

1. `writebackSpreadSeconds` 默认 `1`，允许范围 `1-10`，API omission、新建、读取、保存和显式值 round-trip 一致。
2. `pendingTargetCount` 等于当前 workspace 去重后的实际待写 target 数，不复用 `QueueLength`。
3. 页面在现有区域显示两个字段，不新增独立页面，不显示 D 设置和指标。
4. 最长数字和中文文本在现有桌面/移动布局中不溢出。

## 七、请求时序证据

- `TestPriorityWritebackSpreadDistributesRealSub2APIMutationsWithoutExtraWrites`：`T=30,N=3`，三个秒点各 10 个 Sub2API `bulk-update` POST，共 30 个目标级写请求且不增加写入总数。
- `TestPriorityWritebackSpreadProcessesThirtyTargetsInThreeSlots`：使用 fake clock 证明槽大小 `10/10/10` 和一秒边界。
- 真实 POST 分布测试只证明 E 的出站节奏；authoritative read、generation fencing 和 readback 由独立的 Sub2API 安全测试覆盖，不用测试降级路径替代安全合同。
- 单 target authoritative read/readback 仍可能是主要压力来源；E 仅改变多个 target mutation 的启动分布，不优化单 target 固有成本。

## 八、验证收口

已通过：

- `go test -count=1 -timeout=3m ./internal/modules/connection_health ./internal/modules/upstream ./internal/database/migrations`
- `GOMAXPROCS=2 go test -race -p 1 -count=1 -timeout=3m ./internal/modules/connection_health ./internal/modules/upstream ./internal/database/migrations`
- E 重点回归：分批 `N=1/2/3/5`、非整除、空 batch、跨档/同档窗口隔离、incident 保留与释放、恢复排序、失败槽计数、多实例旧快照、实例接管、`10000/100000`、checkpoint 失败恢复、operation context 取消后的持久化、Sub2API manual generation 与 expected/readback 边界。
- Sub2API 真实 prepared gate：final validation 失败时零 POST，成功时 validation 先于 `bulk-update` POST；字段级请求体仍只包含 `account_ids` 与 `priority`。
- 前端现有 Vitest：`tests/connection-health-priority-sync-preset.test.ts`，5/5 通过，覆盖默认值、1-10 边界、保存 payload 和现有概览字段；临时复用主 worktree 的依赖后 `npm run typecheck` 通过，链接已删除。
- `git diff --check` 通过；每轮 Go/Vitest/typecheck 后确认无测试二进制、fake worker、临时端口或依赖链接残留。固定 `5444` 是原 `dev` 的 Vite 进程；`5555` 在本轮开始前和结束时都未监听。
- 独立只读 review 与本轮提交前复核未发现剩余可修复 P0/P1；已修正分批状态机、manual generation、Sub2API expected 校验、Sub2API 测试辅助路径和前端 E 字段验证，并按项目主站边界剥离 E 新增的 NewAPI 主站逻辑和替代测试。外部上游 NewAPI 的既有行为保持不变。

尚未执行且必须如实保留：

- 没有临时 PostgreSQL 集成环境，因此 migration 仅有 SQL/仓库定向测试，未做空库 `migrations.Run -> EnsureSchema -> roundtrip`。
- 前端没有 Vue 组件挂载测试基础；本轮以现有 Vitest 和 typecheck 验证字段、范围与 SFC 类型，未新增测试框架。
- 用户要求停在提交和本地 `dev` 替换前，因此没有用 `dev-e` 重启或替换固定 `5444/5555` 开发服务；运行态验证留到用户允许 Git 替换后按项目规则执行。
- 上游未提供 revision/ETag/`If-Match`：外部管理员绕过本系统直接在主站修改 Priority 时，仍可能发生在最终 GET 和 PUT/POST 之间而被自动写覆盖。Sub2API 的 repository mutation lease 已封住本系统内的 manual/automatic 边界；外部窗口不能由本地重试、readback 或 generation 宣称为零。

在以上限制公开保留的前提下，E 不因本阶段代码逻辑继续阻塞。Git 提交、本地 `dev` 替换、D 工作区清除、固定服务重启及任何远端发布均等待用户下一次明确许可。
