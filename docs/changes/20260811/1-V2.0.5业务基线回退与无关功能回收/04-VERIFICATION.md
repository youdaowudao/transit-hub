# 验证与收口

## 当前状态

- 目标树以 `origin/dev=9829978` 为父形成唯一的本地 `V2.1.13` 提交，固定本地 `dev` 指向该提交；没有改写远端历史，也没有 push 或创建 PR。
- 固定前后端从主工作区在 `5444/5555` 持续运行；数据库和 Redis 继续使用切换前的 `transithub@127.0.0.1:5432` 与 `127.0.0.1:6379/0`。
- 本目录四份 ignored 文档已按确认边界显式纳入同一提交，没有顺带加入其他 ignored 文件。

## 代码范围证据

- 目标树相对 `V2.0.5 (d193eec)` 的增加项只落在确认保留的手动探活取消、上游管理、站点启用与成本容错、有效倍率、历史显示、主站调度确认、一键升级/回滚、兼容迁移和版本元数据范围。
- 目标树相对 `origin/dev` 的删除和修改项落在 B/C、安全闸门、canary、异常队列、D/E/F、V2.1.10 依附代码及版本元数据范围。
- 禁止签名扫描未发现 `safety_gate`、`safety_epoch`、`canary`、`MutationCoordinator`、`abnormal_queue`、`priority_pressure`、writeback spread、D/F 统计等运行入口。`000019/000020/000021/000023/000024` 只作为兼容迁移保留。
- `000022_upstream_site_enabled.sql`、后端字段读写、前端开关和测试完整保留；数据库已有 `000001` 至 `000024` 记录，候选启动未执行 down migration 或清理。
- 新增旧链回归测试证明 suspended/待恢复档位可从 `100000 -> 1000`、`10000 -> 10`，没有带回 B 的签名、pending 或 generation 机制。
- 版本一致：后端默认版本、前端 package/lock 和生产 compose 镜像均为 `V2.1.13`。

## 独立审核

- 禁止功能与 Git 边界审核：未发现 B/C/safety/canary/D/E/F/V2.1.10 运行残留；候选父级、归档引用和 spacing worktree 均未污染。
- 保留行为审核首次发现两项遗漏：V2.0.9 已连接分组跳转缺少健康页接收端，V2.1.8 主站调度确认缺少回归证据。
- 已只补回原有 `focusGroupId/focusGroupName` 选择与消费逻辑，并补充跳转合同和“确认后才写回”的测试；未修改调度、状态机或数据模型。

## 定向测试与构建

- 后端：`go test -count=1 -p 2 ./internal/modules/connection_health ./internal/modules/upstream ./internal/modules/dashboard ./internal/modules/group_rates ./internal/modules/system`，全部通过。
- 旧 Priority 恢复：`go test ./internal/modules/connection_health -run TestV205PrioritySync_RecoveryLeavesSuspendedAndUnprobedBands`，通过。
- 后端构建：`go build -o <临时目录>/api ./cmd/api`，通过，产物已删除。
- 前端首轮 7 个受影响测试文件共 27 项通过；审核补丁后单独运行分组跳转与调度确认 2 个文件共 5 项，通过。
- 前端：`vue-tsc --noEmit -p tsconfig.app.json` 通过；Vite 生产构建通过，共转换 2495 个模块。Rollup 仅报告 `@vueuse/core` PURE 注释位置警告。
- 部署脚本：`update-source.sh`、`run-source-upgrade.sh`、`rollback-source.sh`、`run-source-rollback.sh`、`run-service-restart.sh` 的 `bash -n` 通过。
- `git diff --check` 通过。测试链接、构建目录、浏览器会话、快照、worker 和临时进程均已回收；固定运行服务除外。
- 一次直接执行 `vue-tsc --noEmit` 因未指定项目文件触发 `TS6310`，按仓库脚本补上 `-p tsconfig.app.json` 后通过；这是命令用法问题，不是代码错误。

## 本地运行验证

### 切换过程

- 第一次切换时候选后端成功，前端因 `npm exec` 参数转发后未监听；命令自动回退。回退前端未成功监听后，已停止两个精确进程组并用原 Vite 二进制恢复主工作区，`5444/5555` 健康正常。
- 修正为直接调用既有 Vite 二进制后再次切换成功。当前唯一监听进程为候选后端和候选前端，没有并行第二实例或替代端口。
- 候选前端运行期间保留一个 ignored `node_modules` 符号链接，目标是主工作区既有依赖目录；它不是提交内容，停止候选实例后再清理。

### 数据与调度

- 切换前基线：2 个 workspace、1 个当前 workspace、4 个启用站点、15 条健康状态、5371 条事件、11 条 Priority checkpoint、4 条目标动作；pending/conflict 均为 0。
- 候选首次 tick 后 workspace、站点和 Priority checkpoint 数量不变，pending/conflict 继续为 0；`target_action_states.pending_status` 是 NOT NULL 空串列，当前 6 行的 status/weight pending 均为空、generation 为 0、source/key 为空，不能用 `count(pending_status)` 判断 pending 数量。旧 safety 队列保持 `cancelled=12/completed=11/queued=2`，候选没有读取或推进 queued 记录。
- 连续观察 90 秒并补跨一轮 tick：事件从 5390 增至 5405，suspended 状态的 `last_probe_at` 更新到 `2026-08-11 05:28:11+00`，证明账号停用后 scheduler 仍继续探活，没有停在 canary。
- 真实探活期间健康分布最终为 `degraded=1/healthy=5/suspended=9`；本地 Sub2API 从 53 active/3 inactive 变化为 50 active/6 inactive。变化符合 V2.0.5“一次硬失败直接停用”的旧行为，账户总数保持 58，Priority 与状态快照在相邻样本间曾连续稳定。
- 继续观察到 `13:37:11`、`13:37:15` 两条 `suspended -> observing`，随后 observing 目标继续收到成功探活；`13:40:11`、`13:40:18` 又有两条 `degraded -> healthy`。数据库全历史另有 `suspended -> observing=7`、`observing -> recovering=2`、`recovering -> healthy=4`，证明完整恢复链曾实际走通。
- 反向证据：目标 `:100` 在 `13:37:11` 进入 observing 后，于 `13:46:17` 又回落 `observing -> suspended`。这说明恢复探活在运行，但该目标本轮没有恢复成功，不能把“恢复链启动”写成“所有目标已经收敛”。
- 后端监控约 27 MB RSS、CPU 约 0.2%；前端约 210 MB RSS、CPU 从 1.9% 回落到 1.4%，未见持续增长。
- 当前策略冷却和观察时间均为 300 秒。数据库历史已经证明完整链路存在；本次候选观察没有单独复演同一目标的完整链路，且记录到一个 observing 后回落 suspended 的未收敛样本。

### 浏览器与 API

- Playwright 隔离浏览器打开候选首页，登录页完整渲染，33 个静态请求正常，控制台 0 error/0 warning。
- 直接访问健康页会被认证守卫送回 `/login`。隔离浏览器没有登录态，现有 Chrome 也未安装 Playwright Extension，因此未完成已认证后台页面点击验收，未改密码、用户或会话。
- `/api/health` 在后端和前端代理均持续返回 `status=ok`；受保护的 `/api/system/version` 未认证时正确返回 401。
- 未触发手动探活、调度开关、一键升级、一键回滚、删除或其它写操作；这些路径以定向测试、类型检查、构建和静态合同验证为证据。

## Git 与未决项

- 固定本地 `dev` 指向以 `origin/dev=9829978` 为父的 `V2.1.13` 提交；归档引用 `archive/dev-v2.1.12-before-v2.0.5-rebuild-20260811` 仍指向旧线 `db436763`。
- 切线前排队登记副本、safety 未提交修复和旧 V2.1.10 WIP 分别按 stash 消息独立保存；未 pop、drop 或 clear。
- `/tmp/transit-hub-health-priority-spacing` 仍为原 16 项暂存改动、`+1235/-69`，未被修改。
- 本轮只有一个本地业务提交；没有 push、force-push、远端引用修改、PR、main、数据库清理或服务器操作。
- 已认证后台页面点击仍未完成；本次候选对同一目标的完整 300 秒恢复链没有单独复演，但数据库历史已有完整恢复证据，二者必须分开表述。
