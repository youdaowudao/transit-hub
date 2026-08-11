# 实施方案

## 1. 总体方法

保留当前 scheduler 的任务生成、target lease、执行前完整刷新、探活和账号级远端动作入口。保底只插入自动 `inactive` 的最终裁决位置：使用 target 已经刷新得到的完整 workspace 清单，在调度轮内协调并发候选，决定本次是否允许写主站。

```text
调度轮初始 inventory
-> target 获得 lease
-> 保留既有执行前完整刷新
-> 探活和本地状态更新
-> 本轮保底裁决
-> 必要时写一次 active/inactive
```

不把远端动作搬到轮末集中阶段，不删除 target 刷新，也不新建队列、worker 或持久状态。

## 2. 保留的执行前刷新

1. `AdminProbeTarget`、`findAdminTargetWithMemberships`、target lease 和 `runAdminProbeJob` 的执行前刷新保持存在。
2. 该刷新继续完整读取当前 workspace 的分组及账号，以得到目标当前 status、schedulable、全部成员关系和有效策略。
3. 将已有刷新过程整理为可同时返回 target、memberships 与本次完整 inventory 的内部结果；floor guard 只能消费该结果，禁止另发请求。
4. 初始 inventory 仍用于生成本轮到期任务；执行前刷新仍用于消除排队、人工修改和成员变化造成的旧快照问题。
5. 任何分组读取失败时，本次目标不得自动 inactive；继续按现有错误和后续到期探活路径处理。

## 3. 调度轮内保底协调

1. 每个 scheduler tick 创建只存在于内存的 workspace floor guard，保存本轮已经预留 inactive 的稳定 target ID。
2. scheduler job 在探活结束、准备按现有账号聚合逻辑写 inactive 时，持有 floor guard 的短互斥锁。
3. 用该 job 已刷新得到的完整 inventory 按 group ID 构造 active target 集合；同一账号在一个分组内按 target ID 去重。
4. 从每个集合扣除本轮已经预留 inactive 的 target，再检查候选目标所属的全部分组：任一分组剩余 active 小于或等于 1，即返回 `skipped_sub2api_group_last_active`。
5. 全部分组均大于 1 时，先把候选 target 加入本轮预留集合，再释放互斥锁并调用现有主站 inactive 写入。
6. inactive 调用失败、超时或结果不明确时，本轮预留不归还，避免不确定写入后继续关空同组账号；tick 结束即销毁预留，后续到期探活重新刷新再判断。
7. 因最后 active 跳过时不得写 pending inactive。上轮 pending inactive 经刷新确认没有生效且本轮又被保护时，清除该 pending。

这样两个并发 job 即使各自在写入前读取到两个 active，后进入 floor guard 的 job 也会扣除前一个已预留账号，只能保留最后一个。

## 4. 状态、恢复与人工路径

- 状态机和探活周期保持不变。最后 active 仍可处于 `suspended`，但后续到期时仍会重新探活、刷新清单并重新裁决。
- 保底跳过只是不发本次 inactive，不得写“已停用”检查点或永久跳过标记。
- 账号自身成功时继续沿用现有 `suspended -> observing -> recovering -> healthy`，不增加安全确认状态。
- 同组另一账号恢复并在执行前刷新中显示 active 后，原保底账号下次到期仍故障时可按正常规则关闭。
- 正式手动探活不直接写 `inactive`，但保留既有的恢复 `active` 写回；一次性隔离探活保持完全隔离。
- 发现分组 active 为 0 时，仅从 checkpoint 能证明健康模块曾关闭、且原始状态为 active 的候选中恢复一个；选择最近系统关停者，并列按 target ID。没有所有权证据不自动开启。
- 同一 tick 中 unmanaged 恢复可能先修改 checkpoint，空分组恢复前重新读取一次本地 target action states；这是本地 PostgreSQL 查询，不是 Sub2API 主站清单读取。

## 5. 事件、数据库与长期事项

- 新动作码区分“最后 active 保底跳过”“清单不完整”和“主站调用失败”；保底事件写明实际阻止关停的 group ID。
- 事件是审计结果，不能作为下一轮调度前提或 legacy ownership 证据。
- 复用现有健康状态、事件、target action checkpoint 和 scheduler runtime lease，不新增迁移，不读取旧 safety/floor/survivor 兼容表。
- 代码内置版本、前端 package/lock、生产 compose 镜像和提交标题统一为 `V2.1.14`。
- 未来读取优化必须单独立项，不能在本次顺带删除刷新；长期记录见 `docs/initiatives/阶段规划/20260811-未完成任务-分组健康调度清单读取优化.md`。
