# TransitHub 适配边界

上游固定版本和原文件摘要见 `UPSTREAM_VERSION`。这是普通源码目录，保留独立 Go module、React 页面、LICENSE 和 THIRD_PARTY_NOTICES；不是 Git submodule。

- `cmd/transit-hub` 与 `internal/hub/` 提供一次性执行和查询。Hub 不运行原 `main.go` HTTP 服务，不启动原匿名历史、24小时清理、worker 或模型库定时同步。
- 数字指纹库改用 Ikaleio/lm-detector 快照（来源见 `data_ref/provenance.json`），用 `scripts/sync-bank.cjs` 手动拉取，再用 `scripts/convert_bank.py` 生成内置库；Claude 型号 ID 沿用原来的横线写法，gpt-6.1-sol 作为 gpt-6-astra 合并类的别名。`local.references.json` 里的单渠道参考已清空。`bank_sync.go` 只同步 ModelTrace 来源的库，来源不同的运行时缓存也不会覆盖内置库。库内模型的指纹采样统一使用 16384 输出上限。
- `internal/api/run.go` 删除把参考答案发回被测模型自评的分支；关键回归在 `hub_no_selfgrade_test.go`。Hub 检测使用本地判题。
- Hub侧D-Bus管理器仅在所有run/query及启动任务均空、整个进程组无程序时回收空slice，避免systemd自动回收限流留下内核记账；创建与空组回收使用固定互斥，无常驻清理器。
- `internal/gateway/` 和各探针中的可选配置保留原独立程序的默认行为；Hub 显式使用重试、输出大小、调用次数、协议及鉴权限制。题目、答案和参考库只在子进程里加载。
- `web/` 的 Hub profile 使用父页受限消息接口，不保存账号 API Key，不调用原匿名/分享/付费问答接口。原界面布局与结果组件继续使用。
- 首次工作区初始化导入全部预设；之后设置和题目以 SQLite 为准。所有预设可查看、编辑和删除，删除后重启或源码升级不重种。旧报告保留题目快照。
- 构建产物只进入已有忽略目录 `.artifacts/`、`web/out-build/`；运行数据位于 Hub 的 `data/chanshui/hub.sqlite`，不随源码更新覆盖。

更新上游时先下载到项目内临时目录，用 `UPSTREAM_VERSION` 原摘要区分上游变化和适配变化，逐个迁移上述适配点，再验证预设全量清单、原行为、协议/判题与真实进程回收。不能直接覆盖整个目录或自动更新生产参考库。代码的 MIT 许可不替代第三方数据许可，继续保留上游来源说明。

新检测API的通用结果采用32KiB流式结束状态封装，父页适配后业务值不变；完整结果附清理警告，缺少完整结束状态时保留旧视图。不会为等待退出把整份报告加载进Hub内存。

统一源码升级：frontend npm prebuild覆盖仍在执行旧inode升级脚本的首次过渡；新升级/回滚内嵌检测函数并持项目构建锁，顺序构建暂存与配对发布，失败恢复原程序/页面。检测函数唯一源deploy/chanshui-build.sh，维护脚本的生成块由测试校验，运行时不source可被回退删除的helper。稳定产物路径和data/chanshui不变，无单独本机构建/Release安装流程。正常Hub默认提供入口，APIOnly保护保留；缺失slice由原D-Bus建立并严格核验，manager按EUID一次选择或沿用明确配置，无提权/降级。
