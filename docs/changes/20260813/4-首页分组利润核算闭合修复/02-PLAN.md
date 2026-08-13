# 实施方案

状态：已实施。本方案以 `01-SPEC.md` 为唯一需求边界。

## 一、先建立稳定的分组营收链

1. 扩展 Sub2API 分组汇总解析，使每条汇总记录同时保留稳定 `group_id` 和金额；不再用分组名称作为归集键。
2. 分组贡献接口针对当前上海业务日只读取一次分组汇总，构造 `displayRevenueByGroup[groupID]`。
3. 所有主站分组均以该映射填充 `todayRevenue` 和兼容营收字段；即使该分组存在真实接入、成员变化、连接级查询失败或连接级营收为零，也不得清空或替换展示营收。
4. 分组汇总读取失败时保留现有可解释的不可用响应和问题记录；不得用连接级金额假装完整分组营收。

## 二、把真实接入先完整分类

1. 以启用站点上的 active `real_connections` 建立 `expected` 集合。inactive 连接和禁用站点连接不进入 `expected`，也不能阻止其他连接成为 `exact`。
2. 对每条 expected 连接建立唯一结果对象，先验证：单一分组、必要 ID、重复 Key、重复账号、主站分组存在且可用、当前账号仍属该分组、连接级营收可读、Key 成本可读。
3. 多分组、重复 Key、重复账号等无法确定归属的情况先自动解绑本地记录；缺 Key、无分组等已确认失效的绑定也自动解绑。账号停用但 Key 仍存在且分组唯一时仍保留并转移。只有上游成员或成本快照不完整、自动处理失败时，才保留带 `connectionId` 的 `failed` 问题。
4. 成本读取以 Key 是否在返回集合中判断存在性，不能把金额 `0` 误判为缺失。
5. 连接级营收按 `(account_id, group_id)` 去重查询，再分别回填到属于该账号和分组的连接；查询失败时相关连接逐条记为 `failed`，不能只给一条连接计数。
6. 主站成员关系变化按完整成员快照处理：唯一当前成员且 Key 存在时转移 `real_connections` 的本地自有分组并同步价格映射；账号停用不触发解绑。无分组、多分组、重复绑定或 Key 缺失的记录走本地 `unlink`，不触碰上游资源。

## 三、完成连接后再聚合分组

1. 先把所有连接终态固定，再按 `group_id` 聚合 `exact` 连接的 Key 成本与连接级营收。
2. 连接利润仅由本连接营收减本连接成本得到；分组成本只由该分组所有 exact 连接成本求和。
3. 对每个分组检查：该组 expected 连接是否全部 exact，以及连接级营收合计是否与 `displayRevenueByGroup[groupID]` 在共享精度内一致。
4. 通过时返回该组 `todayCost` 与 `todayProfit = displayRevenue - aggregatedCost`；不通过时将两者置为 `null`，并保留营收、连接状态和问题。
5. 不再让分组状态、成本或利润随着连接遍历覆盖；迭代顺序不得影响任何 API 字段或质量计数。
6. 将 `expected`、`resolved`、`failed`、`unallocatable` 从同一份连接终态派生，并在返回前断言闭合。不闭合是实现错误，不能用临时修正计数掩盖。

## 四、全局与前端边界

1. 全部 expected 连接闭合时，返回分组贡献汇总 `exact`、`totalCost` 和 `totalProfit`；汇总成本、利润分别为正式分组贡献与分组外上游成本贡献之和。
2. 否则全局状态为 `partial` 或 `unavailable`，`totalCost` 和 `totalProfit` 均为 `null`；不因某些精确分组存在而伪造分组贡献汇总。未绑定上游成本单列为利润贡献，不归属任何主站分组，且不进入连接问题数。
3. 后端可在 `partial` 返回该精确分组的利润，但 `profitAvailable` 的语义必须与全局总利润边界一致，不能再成为前端利润柱的唯一门槛。
4. 前端利润模式仅筛选 `status === 'exact' && todayProfit != null`；营收模式不受该限制。前三分组利润占比仅在正式 `totalProfit` 可用时计算。
5. 复用现有页面布局、图表和样式，只收紧数据筛选条件与必要的状态文案，不做页面重设计。

## 五、预计修改点

- `backend/internal/modules/upstream/types.go`：分组汇总 DTO 增加稳定分组 ID。
- `backend/internal/modules/upstream/platform_service.go`：解析日期正确的 Sub2API 分组汇总 ID 与金额，并补协议测试。
- `backend/internal/modules/dashboard/real_group_usage.go`：拆分展示营收、连接营收、终态分类、分组聚合和全局质量。
- `backend/internal/modules/dashboard/real_profit_attribution.go`：改为终态和聚合辅助逻辑，移除单连接覆盖分组金额的行为。
- `backend/internal/modules/dashboard/metrics_types.go`：仅在现有响应无法表达稳定分组状态或问题归属时，做最小契约调整。
- `backend/internal/modules/dashboard/*_test.go`：补充归因、接口和质量闭合测试。
- `frontend/src/modules/admin/views/DashboardView.vue`：利润柱二次过滤；必要时更新本地类型。

## 风险与回滚

- 变更会修正已展示金额，发布前必须保留同一业务日的分组营收、连接成本、分组成本和利润对账记录。
- 若部署后发现分组汇总接口不返回可用 `group_id`，停止发布；不得回退到名称匹配或连接级营收作为展示权威。
- 若任何定向验证未能证明“部分分组不展示利润、精确分组仍显示利润”，停止在本地验证阶段，不提交、不部署。
- 代码回滚只回滚本变更涉及的源码；不修改真实连接、主站分组、缓存或数据库数据作为回滚手段。
