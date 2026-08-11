# 执行任务

## A. 前置保护

- [x] 从 `docs/changes/排队.md` 领取本任务并标为开发中；确认没有并行开发任务。
- [x] 核对 `dev`、工作区状态、固定服务归属和当前版本为 `V2.1.13`。
- [x] 记录调度轮初始 inventory、target 执行前刷新及远端动作的现有调用关系。
- [x] 确认本次只处理 Sub2API 健康模块自动 status 动作，不修改 Priority、schedulable 或状态机参数。

## B. 保留刷新并传递清单

- [x] 保留 `AdminProbeTarget`、target lease 和 `runAdminProbeJob` 执行前完整刷新。
- [x] 让已有刷新结果同时提供 target、memberships 和完整 inventory，禁止 floor guard 发起新读取。
- [x] 保留初始 inventory 的任务生成职责，不改成单一 tick 快照执行。
- [x] 分组读取不完整时禁止当前次自动 inactive，不用猜测成员关系。

## C. 最后 active 保底

- [x] 在 scheduler tick 内建立短生命周期 workspace floor guard；不得写数据库或引入队列。
- [x] 每个 inactive 候选在已有刷新后、主站写入前，通过 floor guard 检查全部所属分组。
- [x] 允许关停时先预留 target，再调用现有 inactive 写入；并发 job 必须共享同一预留集合。
- [x] inactive 调用失败或结果不明确时，本轮不归还预留；下一次到期探活重新刷新和判断。
- [x] 保底跳过不写 pending inactive、不写 last-applied inactive，不改变健康状态和探活时间。
- [x] 上轮不明确写入经新刷新确认仍 active、且本轮触发保底时，清除旧 pending inactive。
- [x] 增加可读动作码、事件和实际阻断分组信息；这些字段不得参与调度资格判断。

## D. 恢复与边界

- [x] 保底账号继续走既有 suspended/observing/recovering/healthy 状态机与到期探活。
- [x] 正式手动探活不直接写 `inactive`，保留既有的 `active` 恢复；一次性隔离探活保持不变。
- [x] 分组为 0 active 时，只恢复有健康模块关停所有权证据的账号；人工 inactive 保持不变。
- [x] 不修改或替换执行前刷新；读取优化只登记长期事项，不混入本次。

## E. 定向自动测试

- [x] 单账号分组连续多个到期探活：inactive 调用为 0，探活和状态更新时间持续推进。
- [x] 两个 active 同轮失败：最多一次 inactive；三个 active 同轮失败：最多两次 inactive。
- [x] 多分组共享账号在任一分组为最后 active 时被保护。
- [x] 两个 job 使用各自旧 refresh 结果并发进入：预留集合仍保证最多关闭到一个 active。
- [x] 同组另一账号恢复后，原保底账号的下一次到期探活刷新确认 active 增加，随后可正常关闭。
- [x] 保底账号恢复时完整推进 observing、recovering、healthy，不受跳过事件影响。
- [x] inactive 写入成功、明确失败、超时和写后进程中断均不会导致同轮继续关空分组；下一次到期探活可重新裁决。
- [x] 不明确写入后下一次刷新成为最后 active：旧 pending 被清除，后续探活和裁决继续推进。
- [x] inventory 不完整时不写 inactive；恢复完整后的后续到期探活重新裁决。
- [x] 已归零分组只恢复系统关停账号，不恢复人工 inactive 账号。
- [x] fake 主站统计表明 floor guard 没有新增 inventory 请求或 status 写后 readback，既有执行前刷新仍发生。
- [x] 正式手动失败不写 `inactive`、不建立关停 checkpoint；正式手动恢复保留既有 `active` 写回及失败记录。

## F. 本地收口

- [x] 运行受影响 Go package 的定向测试和构建，不运行全仓测试。
- [x] 每条测试结束立即清理 fake worker、临时端口、目录、日志和后台进程。
- [x] 按固定服务规则核对并重启 `5444/5555`，确认工作目录、环境和数据库/Redis 目标未变化。
- [ ] 观察至少三个 scheduler tick，并覆盖一个被保底目标的实际到期探活；已完成三个 tick 观察，但未向真实主站制造最后 active 故障，保底分支由定向测试覆盖。
- [x] 核对主站调用相对当前基线没有因保底逻辑新增清单读取或 readback。
- [x] 不向真实主站注入故障或批量修改账号；需要破坏性运行验证时另行取得用户授权。
- [x] 更新 `04-VERIFICATION.md` 的实际证据、剩余风险和收口结论。
- [x] 回到 `docs/changes/排队.md` 完成任务交接；提交前另行取得用户许可。
