# 验证记录

## 实际改动

- 现有 `/admin/user-last-used` 页面增加“最后使用记录 / 充值记录”切换，不增加菜单、路由或页面。
- 后端固定读取 Sub2API `type=balance`、`status=used` 的兑换码分页，排除 `admin_balance` 和非正数记录，按 `used_by` 聚合真实邮箱、次数、余额和最近兑换时间。
- 全量查询增加请求取消、总计 30 秒超时、单页 8 秒超时、单页 2 MiB 响应限制、10 万条上限、重复记录和分页总数变化校验；失败不返回部分数据。
- 两种查询的上游请求均承接浏览器请求取消；邮箱复制状态共用本地持久化标记，刷新后仍保留且复制按钮可重复使用。

## 验证结果

- `cd backend && go test ./internal/modules/mass_email ./internal/modules/upstream`：通过。
- `cd frontend && npm test -- user-last-used.test.ts`：11 项通过。
- `cd frontend && npm run typecheck`：通过。
- `cd frontend && npm run build`：通过；仅有依赖 `@vueuse/core` 既有 Rollup 注释警告。
- `git diff --check`：通过。
- 重启后 `5444`、`5555` 各只有一个当前项目实例；前端页面返回 200，后端健康接口返回 `status=ok`，未登录访问新接口返回 401。
- 重启前后 PostgreSQL 均为 `transithub / postgres / /var/lib/postgresql/data`，数据计数均为 users=1、admin_accounts=2、current_workspaces=1、upstream_sites=4、real_connections=13、connection_health_states=15；当前工作区 ID 未变化，Redis 均返回 PONG。

## 浏览器边界

内置浏览器没有 TransitHub 登录态且没有其他可复用的已登录浏览器，因此未冒用用户凭据验证真实上游数据展示。页面切换、接口调用、邮箱字段、复制持久化和单路由约束已由定向测试覆盖；真实登录态下的最终数据展示仍需用户在现有页面刷新后确认。
