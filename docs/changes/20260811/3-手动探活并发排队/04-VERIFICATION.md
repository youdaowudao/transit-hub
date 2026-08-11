# 验证记录

状态：代码、定向验证和固定服务重启核对已完成。

## 验证项目

- 并发限制器、正式探活流式进度定向测试
- connection health 包定向测试
- 前端取消/进度行为测试、`typecheck`、构建检查
- diff 格式检查
- 固定前后端重启与健康检查
- 重启前后数据库身份、workspace 与关键数据数量核对

## 已完成结果

- `go test ./internal/modules/connection_health -count=1`：通过。
- `go test -race ./internal/modules/connection_health -run 'TestProbeLimiter_|TestProbeTarget_QueuedManualProbeStopsWhenRequestIsCancelled' -count=1`：通过。
- `npm test -- --run tests/connection-health-manual-probe-cancel.test.ts`：3 项通过。
- `npm run typecheck`：通过。
- `npm run build`：通过；仅有依赖包既有 Rollup 注释提示。
- `git diff --check`：通过。
- 前端 `5444` 返回 HTTP 200，后端 `5555/api/health` 返回 `status=ok`；两端各只有一个项目实例。
- 浏览器 smoke 检查确认页面标题为 `TransitHub` 且正文非空；当前无可用登录态，未在浏览器中人工制造满并发后点击验证，真实排队阶段由前后端定向行为测试覆盖。
- 重启后数据库身份仍为 `transithub|postgres`；`users=1`、`admin_accounts=2`、`upstream_sites=4`、`real_connections=13`、`policies=3`、`model_targets=3`、`health_states=15` 保持不变。`health_events` 从初始 7742 增至最终 7787，为服务恢复后调度器自然新增事件。
