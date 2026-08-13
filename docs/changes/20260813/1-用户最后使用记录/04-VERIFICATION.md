# 验证记录

状态：已完成。

## 实际验证

- `go test ./internal/modules/upstream ./internal/modules/mass_email -count=1`：通过。
- `npm test -- user-last-used.test.ts`：通过，`7` 个用例覆盖默认两日、日期去重、上海跨日归组、秒级时间、分页停止、逐行复制契约和上游鉴权区分。
- `npm run typecheck`：通过。
- `npm run build`：通过；仅有依赖包 `@vueuse/core` 的既有 Rollup 注释警告。
- `git diff --check`：通过。
- 页面与工具函数中检索不到 `last_active_at`，新增页面请求未出现 `POST`、`PUT`、`PATCH` 或 `DELETE`。

## 运行验收

- 重启前确认前端为当前仓库 `frontend/` 下的 `npm run dev -- --port 5444 --host`，后端为当前仓库 `backend/` 下的 `go run ./cmd/api`。
- 后端继续使用 `PORT=5555`、PostgreSQL `postgres@localhost:5432/transithub` 和 Redis `127.0.0.1:6379/0`；没有创建、切换或修改数据库与容器。
- 重启后 `5444`、`5555` 均只有一个当前项目实例，`GET /api/health` 返回 `status=ok`，前端 `/admin/user-last-used` 返回 `200`。
- PostgreSQL 容器、数据目录、数据库名和用户保持不变；重启前后均为 `users=1`、`admin_accounts=2`、当前 workspace 数 `1`、`upstream_sites=4`、`real_connections=13`、`connection_health_states=15`，当前 workspace ID 未变化。
- Redis 重启前后均返回 `PONG`。

## 浏览器验收

- 在实际本地前端页面中，仅用浏览器网络路由伪造只读用户列表，没有向 TransitHub 或 Sub2API 写入测试数据。
- 页面默认显示 `2026-08-13` 和 `2026-08-12`，伪造记录按 Asia/Shanghai 正确归为 `2` 人和 `1` 人。
- 核对了 `2026-08-13 00:00:01` 与 `2026-08-12 23:59:59` 的跨日边界；空用户名和空 `lastUsedAt` 均未展示。
- 两个不同复制按钮分别向 `navigator.clipboard.writeText` 提交 `alice.api` 和 `charlie`。
- 浏览器网络记录只有 GET，用户请求为 `page_size=100&sort_by=last_used_at&sort_order=desc&timezone=Asia/Shanghai`。
- `390x844` 手机视口下页面无整体横向溢出，日期输入与添加按钮不重叠，表格在自身容器内横向滚动。

## 现场说明

- 浏览器会话已关闭，无 Playwright、Vitest 或类型检查进程残留；最新 `5444`、`5555` 开发实例继续运行。
- 清理本轮 `.playwright-cli` 临时文件时，误删了该 Git 忽略目录中此前遗留的历史 YAML 快照和日志。受影响内容不在 Git 中，无法通过 Git 恢复；源码、数据库、运行配置和当前服务未受影响。
