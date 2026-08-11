# 执行任务

## A. 前置保护

- [x] 记录精确主工作区路径、分支、三个关键远端/本地 SHA 和所有 worktree。
- [x] 为 `db436763` 建立仅本地归档引用。
- [x] 分别保存当前 `repository_safety_incidents.go` 未提交 diff 和已有 `stash@{0}`，校验两者不是同一份内容。
- [x] 确认 `/tmp/transit-hub-health-priority-spacing` 的 16 项暂存改动保持原样。
- [x] 建立第三个隔离 worktree；不得在主工作区直接物化大范围回退。

## B. 目标树重建

- [x] 以 `origin/dev=9829978` 为父创建本地临时重建线路。
- [x] 恢复 `V2.0.5` 业务代码和 A 行为。
- [x] 删除 B、C、安全闸门、canary、异常队列、D、E、F 及 V2.1.10 写回窗口调用链。
- [x] 不移植 `79b384c`；确认其 D 代码完全缺席，并以 V2.0.5 旧链测试 `10000/100000` 恢复，不带回 B 的签名/pending 修复。
- [x] 收回 V2.0.8、V2.0.9、V2.0.11、V2.1.0、V2.1.2、V2.1.6、V2.1.7、V2.1.8、V2.1.9 的无关行为。
- [x] 完整收回一键升级/回滚的后端、前端、部署脚本、systemd 单元、测试和迁移兼容元数据。
- [x] 单独保留 `000022_upstream_site_enabled.sql` 及站点启用的后端读写、前端入口和测试；不得把它归为无效残留。
- [x] 仅把 `000019/000020/000021/000023/000024` 作为兼容迁移保留，运行代码不得恢复 B/C/safety/E/F；D 专用迁移不重新加入源码。
- [x] 保留历史 docs 和项目治理文件；新文档明确旧安全方案已退出，不删除审计材料。
- [x] 将代码、前端 package、compose 镜像和提交标题统一到 `V2.1.13`。

## C. 静态遗漏检查

- [x] `rg` 检查 `safety_gate`、`safety_epoch`、`canary`、`MutationCoordinator`、`incident`、`abnormal_queue`、`priority_pressure`、`writebackSpread`、`DEnabled`、`F` 等禁止运行入口。
- [x] 区分仅存在于历史文档、迁移兼容文件和测试说明中的文字；禁止以文档字符串误判为运行代码残留。
- [x] 检查所有 `connection_health` scheduler、service、repository、types、routes 是否回到 V2.0.5 旧合同。
- [x] 检查 merge commit 没有被当成功能提交 cherry-pick；最终内容只由白名单行为决定。
- [x] 检查一键升级和一键回滚的 API、executor、unit、脚本和前端调用互相匹配。
- [x] 检查迁移 runner 对 000019-000024 的行为：跳过已应用版本且不执行反向操作；另行证明 `000022` 对应的站点启用读写仍在运行。
- [x] 生成目标树相对 `V2.0.5` 和相对 `origin/dev` 的双向差异报告，逐项归类所有增加、删除和修改。

## D. 本地验证

- [x] 后端运行受影响 package 的定向测试和构建。
- [x] 定向覆盖 suspended/待探活恢复后的旧 Priority 收敛；若失败只记录阻断，不在本次发明新机制。
- [x] 前端运行受影响行为测试、类型检查和构建。
- [x] 对 system upgrade/rollback 运行对应定向测试；不启动真实 SSH、Docker、systemd 或外部服务。
- [x] 检查测试结束后无临时 worker、端口、日志、环境变量或后台进程残留。
- [x] 校验最终版本、路径白名单、禁止路径和目标树差异报告。
- [x] 对相对 `origin/dev` 被删除但属于保留清单路径的内容逐项解释，并用功能测试证明没有误删。

## E. 提交前收口

- [x] 报告暂存区、未暂存区、未跟踪文件和计划纳入提交的完整路径。
- [x] 将本变更文档和排队登记纳入文档交接范围；不把服务器或远端操作混入本次提交。
- [x] 核对 `docs/` ignore 规则；经用户确认提交边界后显式加入本目录四份文档，禁止遗漏或顺带加入其他 ignored 文件。
- [x] 用户确认后创建唯一的 `V2.1.13` 中文提交；本轮不 push、不建 PR。
- [x] 提交后仅报告本地 SHA、工作树状态和验证结果，等待用户自行远端操作。

## F. 后续动态间距迁移交接

- [x] `V2.1.13` 完成前保持 spacing worktree 的 16 项暂存改动原样。
- [ ] 回退完成后从新的本地 `dev` 建立独立迁移分支，不把旧分支 merge 到新线。
- [ ] 以已确认 SPEC 和 staged diff 为来源，在 A 基线上重新实现动态间距。
- [ ] 禁止带回 B/C/E/safety 的 checkpoint、分批写回、pending 签名和 mutation generation。
- [ ] 新旧行为逐项对照通过后再申请后续提交许可。
