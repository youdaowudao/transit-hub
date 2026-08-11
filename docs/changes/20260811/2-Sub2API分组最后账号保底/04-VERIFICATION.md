# 验证与收口

## 当前状态

- `V2.1.14` 代码、定向测试、本地重启和持续观察已完成，当前停在提交前。
- 未向真实 Sub2API 注入故障；最后 active 拦截由自动测试证明，真实实例只做只读观察。
- 保留 target 执行前完整刷新；floor guard 只消费该结果，没有新增主站清单读取或写后 readback。
- 独立审查发现手动探活的 `active` 恢复曾被过度门控；已收窄为只禁止手动 `inactive`，恢复原有 `active` 写回并重跑定向测试。

## 核心不变量

| 不变量 | 必须提供的证据 | 当前结果 |
| --- | --- | --- |
| 每个分组至少保留一个 active | 2/3 active 并发测试分别只允许预留 1/2 个 inactive | 通过 |
| 跨组目标按任一分组否决 | 单成员组和多成员组共享 target 测试 | 通过 |
| 保底后继续探活 | 连续批次测试；本地事件和 last probe 三轮持续推进 | 通过 |
| 每次重新判断 | 新 tick 新 guard、清单增加 active 后重新允许关停的测试 | 通过 |
| 无持久安全门 | 无迁移、无 safety/canary/queue；guard 只存在于 tick 内存 | 通过 |
| 不增加主站负担 | 单 target 计数测试证明 refresh + guard 为 1 次 groups + 2 次 accounts；scheduler 空组恢复路径复用 inventory cache | 通过 |
| 写入异常不关空且能继续 | 真实 dispatcher error 测试保留本轮 reservation；新 tick 重新判断 | 通过 |

## 实际改动

- `refreshAdminTarget` 保留原 target 执行前刷新，并把同一次完整 inventory 交给 floor guard。
- scheduler 每个 workspace/tick 共享一个内存 reservation 集合；允许 inactive 时先预留，调用失败也不在本轮归还。
- 最后 active 或清单不完整时跳过 inactive、清除旧 pending inactive，并记录阻断 group ID；跳过结果不覆盖上次真实远端动作。
- 已归零分组只从 `original=active`、`last_applied=inactive`、无 pending/conflict 的系统 checkpoint 中恢复一个；人工 inactive 不动。
- 正式手动探活不直接写 `inactive`，但保留既有的 `active` 恢复；一次性隔离探活仍不触发远端动作。
- scheduler 在 unmanaged 恢复之后为空分组恢复再读一次本地 target action states，避免使用已被前一步修改的 checkpoint；该查询不访问 Sub2API。
- 前端为两个新动作码增加可读中文文案；版本四处统一为 `V2.1.14`。
- 没有新增迁移、数据库表、队列、worker、safety gate、canary 或持久 reservation。

## 自动验证

- `go test ./internal/modules/connection_health -count=1`：通过。
- `go test -race ./internal/modules/connection_health -run 'TestWorkspaceFloorGuard_ConcurrentCandidatesNeverReachZero' -count=1`：通过。
- 审查修正后，手动 `inactive` 拦截、手动 `active` 恢复成功/失败和并发 floor guard 用例在定向及 race 模式下通过。
- `go build -o /tmp/transit-hub-v214-api-check ./cmd/api`：通过，产物已清理。
- `npm run typecheck`：通过。
- `git diff --check`：通过。
- 定向用例覆盖单账号保底、2/3 active 并发、跨组否决、旧快照交错、写入失败、本轮 reservation、新 tick 重算、旧 pending 清理、inventory 不完整、归零恢复、人工 inactive 保护、手动探活边界、事件 group ID 和主站调用计数。
- 每轮测试后均确认无 `go test`、race、`vue-tsc`、httptest、临时二进制或测试端口残留。

## 本地运行验证

- 审查修正后最终前端 PID `421001`、后端 API PID `421187`，工作目录分别为当前仓库 `frontend/`、`backend/`；`5444/5555` 唯一监听且两端 `/api/health` 均为 `ok`。
- PostgreSQL 仍为 `transithub/postgres`、数据目录 `/var/lib/postgresql/data`；Redis 仍为 `127.0.0.1:6379/0` 且 `PING=PONG`。
- 重启前后用户 1、workspace 2、站点 4、真实连接 13、健康状态 15，workspace 指纹一致。
- 三组 30 秒采样中事件数 `6730 -> 6738 -> 6744 -> 6746`，最新探活时间连续推进；状态从 `suspended=8, observing=1` 推进到 `suspended=6, observing=3, healthy=4`。
- 运行期间目标 `:101` 完整走过 `observing -> recovering -> healthy`，真实执行一次 `sub2api_account_status_active`；其恢复完成后 checkpoint 从 6 正常释放到 5，pending/conflict 始终为 0。
- 审查修正重启后只读核对：事件和 `last_probe_at` 继续推进；`gpt pro` 为 12 个账号（5 active、7 inactive），`GROK` 为 1 个 active，有成员分组的 `zero_active=0`。target action states 为 7，pending/conflict 均为 0。
- 独立 Playwright 会话能打开本地登录页且无控制台告警，但没有现成登录态；未猜密码或绕过认证，浏览器与本轮产物已清理。
- 自然流量没有触发 `skipped_sub2api_group_last_active`。未制造真实最后账号故障；该分支以定向测试为提交证据。

## 收口判定

代码、自动验证和非破坏性运行观察满足提交前条件。没有恢复旧安全阀门体系，没有改变 Priority、schedulable 或状态机参数，也没有新增主站读取。

剩余证据限制只有两项：未在真实 Sub2API 制造最后 active 故障，未完成登录后页面人工验收。二者不构成已知代码缺陷；前者由完整定向测试覆盖，后者因独立浏览器没有登录态而保留说明。当前已按用户要求停在提交前。
