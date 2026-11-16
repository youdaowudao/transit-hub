# 构建与启用

本功能在浏览器里显示页面，向模型发请求的检测器在服务器按需执行。没有空闲检测 daemon 或新监听端口；页面收起/关闭不会取消任务。报告保存后检测器自行退出，报告和题库长期保存在 SQLite。

在构建机器运行 `bash scripts/build-chanshui.sh`（从项目根目录）。需要 Go 1.25、Node 22 或更高的兼容版本、npm、Python3；脚本把缓存放在项目里，不安装系统服务。部署产物是：

- `chanshui/.artifacts/bin/transithub-chanshui`
- `chanshui/web/out-build/`，包含构建时生成的 `bank-manifest.json`
- 本目录的 `transithub-chanshui.slice`

生产服务器不需要安装检测页面的 Node/Next 服务。现有 Hub 源码升级脚本的旧构建流程保持；独立检测产物必须在构建机器制作后随发布包交付。Docker 构建也打入这些产物，但默认不启用本功能；普通容器若没有经过配置的 systemd/cgroup 执行环境，维持关闭，不降级成无限制进程。

启用前须按主项目服务器规则逐条审核安装/配置/服务重启命令，本文件不授权现场操作。系统或用户 manager 必须可访问，cgroup v2 的 memory/pids 控制器可用；将 slice 配置装入选定 manager 并验证父级限制可用。slice 不创建进程，只为 run/query 及后代设置合计 500000000 字节、swap=0、TasksMax=64。`GOMEMLIMIT` 是补充软限制，不能替代硬限制。

Hub 配置：

| 配置 | 含义 |
| --- | --- |
| `CHANSHUI_ENABLED` | 默认 false；产物、路径、权限和实际资源验收通过后才开启 |
| `CHANSHUI_BINARY_PATH` | 当前项目部署目录内的可信绝对二进制路径，不允许网页上传覆盖 |
| `CHANSHUI_WEB_DIR` | 当前项目内 `web/out-build` 的绝对路径 |
| `CHANSHUI_DATA_DIR` | 当前项目内 `data/chanshui` 的绝对路径；目录0700、SQLite/WAL/SHM文件0600 |
| `CHANSHUI_SYSTEMD_MANAGER` | 生产通常 system，本机验收 user；缺权限拒绝检测，不自动提权 |
| `CHANSHUI_TEST_ALLOW_PRIVATE_TARGETS` | 默认false；仅隔离开发模拟允许开启，不进入正式配置 |

模型 Key 仅在服务器核验账号后通过 stdin 传给已验证限制的检测器，不进入网页、URL、环境变量、命令行或报告。模型下拉按当前账号的列表预填，列模型不代表已开始付费检测。

验收先运行 `make test-chanshui`；真实资源门禁需显式设置 `TRANSITHUB_CHANSHUI_RESOURCE_TEST=1`，使用当前项目的 user manager 和隔离数据，不访问真实模型。普通测试和 CI 不会默认启动 systemd 单元。还须在项目唯一5444/5555和固定专用浏览器profile核对两个入口、全量预设及增删、账号/工作区切换、历史和停止/关闭行为。

任务内存可设128～384MiB，默认256MiB；短查询64MiB，始终共同受0.5GB上限。超过限制只应结束本模块；错误必须说明已保存范围和可能产生的费用。正常完成、停止、超限后都要确认整个进程组退出；无法确认时暂停新任务，不自动重新付费执行。

SQLite 备份使用在线backup或停止写入后的完整一致备份，不能只拷贝活跃WAL模式下的主文件。回退先禁用入口、停止本模块执行者，保留SQLite及备份，再换回产物；不清理Hub旧数据库、不自动删除报告。
