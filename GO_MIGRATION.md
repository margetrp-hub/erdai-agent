# 二呆智能体 Go 迁移与运行边界

> 当前源码与公开发布口径以 [`CURRENT_RELEASE.md`](./CURRENT_RELEASE.md) 为唯一机器可读事实源。生产主机、网络地址、备份路径和真实账号验收记录不写入公开仓库，保存在私有运维记录中。下文历史段落中的 `0.9.4-runtime-instances-r2`、`Schema v59` 仅用于迁移背景，不代表当前发布版本。

## 2026-08-24 二呆四主题 UI

- **VERIFIED-CURRENT / 发布范围。** 现代 WebUI 提供四个独立风格：`native` 原生、`standard` 标准、`anime` 二次元、`industrial` 废土工业；选择结果写入浏览器本地存储并在登录页与控制面保持一致。当前 Core 使用 SQLite schema v74；v74 仅为未被管理员改写的小满 1.3.0 视觉卡补回与豆包身份隔离约束，并升级为角色卡 1.3.1。
- **VERIFIED-CURRENT / 视觉与适配。** 原生沿用当前深色控制面；标准为中性浅色工作台；二次元使用星际中继背景和导航角色；废土工业使用工业废墟背景、操作员图像、锈橙信号色和深色高对比面板。主题图标、背景和角色图均随 Core 镜像同源嵌入，不依赖外部 CDN。
- **VERIFIED-CURRENT / 验收。** 本地 Vite 构建与聚焦 Go 嵌入资源测试通过；生产部署脚本完成 `off -> 健康/Schema/API/临时写读删/QQ connector -> active` 闭环。真实管理员会话中四主题均可切换并生成截图；`390px` 移动端回读 `scrollWidth=390`，无横向溢出。生产容器 `healthy`、重启 `0`、`OOMKilled=false`、用户 `1000:1000`、只读根文件系统为 `true`。
- **BOUNDARY.** 完整 Go 回归本轮未运行，未发送真实 QQ 测试消息；主题默认值仍由每位浏览器本地选择决定，未强制覆盖现有管理员偏好。

## 当前架构

二呆智能体保持纯 Go 单进程 Core。角色卡、运行策略、运行实例、平台连接器和会话路由是不同层级：

```text
Core 安全与公共能力
  -> 运行策略模板
  -> 运行实例
  -> 角色卡
  -> 平台连接器账号
  -> 会话路由
```

- 角色卡只描述人物、表达、视觉、世界书和场景样本。
- 运行策略模板保存模型、工具、主动参与、回复和记忆默认值。
- 运行实例引用一张角色卡和一个策略模板，拥有独立记忆命名空间。
- 平台连接器只处理账号协议、入站标准化、出站投递和健康状态。
- 会话路由决定某个连接器或会话由哪个运行实例接管。
- Core 安全上限始终高于策略、实例覆盖和角色卡；实例覆盖只能收紧权限。

## Schema v59

新增四张通用运行表：

- `agent_policy_templates`
- `agent_instances`
- `agent_instance_connectors`
- `agent_instance_routes`

事件、Run、附件、搜索实体、记忆、关系和媒体配额使用实例级运行作用域。规范键包含：

```text
agentInstanceId + memoryNamespace + transport + transportInstance
+ conversationRef + threadKey + senderRef
```

新实例默认以自身 ID 作为 `memoryNamespace`。旧 QQ 迁移为 `doubao-qq` 实例时保留 `legacy-default` 命名空间，保证原有豆包会话可以连续读取；新实例不会写入旧作用域。

## 路由优先级

1. 连接器实例 + 精确会话
2. 连接器实例 + `*`
3. 平台类型 + 精确会话
4. 平台类型 + `*`
5. 旧角色绑定兼容路径

命中已停用运行实例时，Core 返回 `agent_instance_disabled`，不会静默回退到其他角色。

## 旧 QQ 迁移

Schema v59 会在旧配置存在时创建：

- 策略模板：`doubao-default`
- 运行实例：`doubao-qq`
- 连接器绑定：`qq_official`
- 默认路由：`qq_official + *`

旧 `integration_settings.qq_official` 暂时保留为兼容事实源。发布必须经过 `off -> shadow -> active`，验证入站、角色选择、模型、Outbox、发送、ACK、重启恢复和历史记忆连续性后，才能退休旧入口。

## 多实例边界

- 一个 Core 可以运行多个实例；每个实例可以绑定不同角色卡和连接器账号。
- 同一会话 ID 出现在两个 QQ 或 QQ/TG 时，不得共享附件、近期上下文、关系、搜索实体或配额。
- 豆包和小满是角色卡，不是 Core 中的专属代码分支。
- 小满个人 QQ 应通过 OneBot v11 侧车登录，Core 只接收反向 WebSocket。账号密码不得写入 Core、SQLite、WebUI、日志或发布包，优先使用二维码和设备确认。
- `telegram_user` 的 MTProto 连接器源码保留，但当前不创建启用实例、不启动会话、不计入生产可用能力。

## 连接器事实

当前 factory 有 19 种连接器类型。源码实现和协议测试不等于真实账号可用；后台需要分别显示源码、协议、沙箱和真实账号 canary 状态。

QQ Official 是当前已有生产链路。其余连接器只有在真实凭据下完成认证、入站、文本、媒体、重连、去重、失败重试和 Outbox ACK/FAIL 后，才可标记为生产已验收。

## 发布流程

所有构建和测试只在 VPS Docker 中执行：

```text
同步待发布源码
  -> gofmt
  -> go test ./...
  -> go vet ./...
  -> 构建精确版本镜像
  -> 复制数据库到 shadow
  -> Schema/quick_check/API/WebUI 验收
  -> channel off + drain
  -> 创建临时 SQLite 回滚点
  -> 切换新容器
  -> shadow/active canary
  -> 重启恢复检查
  -> 删除临时回滚点和旧镜像
```

部署使用锁，VPS 不执行源码构建以外的并发重型任务。Embedding 与 Core 的内存上限总和必须低于物理内存，并保留启动峰值空间。

临时回滚点只保留到健康、Schema、QQ 链路和重启检查通过；稳定后按用户要求删除。不使用 `docker system prune`，不触碰 Grok2API、CPA、CLI Proxy 或其他业务数据卷。

## 验收标准

- `go test ./...`、`go vet ./...` 通过。
- SQLite `quick_check=ok`，Schema 为 v59。
- WebUI 运行中心真实读取实例、策略、连接器和路由 API。
- 旧 QQ 自动映射到 `doubao-qq`，角色和历史记忆不变。
- 两个连接器使用相同会话 ID 时，记忆、关系、附件、搜索和配额不串线。
- 停用实例不创建模型 Run 或 Outbox。
- 容器 `healthy`、`RestartCount=0`、`OOMKilled=false`。
- Telegram MTProto 保持停用，除非后续单独完成资源评估和真实账号验收。

## 诚实与时序契约(Schema v69 / 0.10.0 起)

2026-08-21 实施,对应 [二呆人性智能评估与升级方案](../../../assistant/traces/2026-08-21-二呆人性智能评估与升级方案.md) 的 P0-P2:

- **会话串行**:同一 `conversation_ref` 同时只允许一个 `running` 生成;worker 认领带 NOT EXISTS 互斥与 10 分钟 stale 保护,消灭同群乱序投递。终态入队和取消路径会 `signalWorker` 唤醒被让路的 run。
- **过期终态废弃**:终态入队在同一事务内检查"同会话同发送者更新的 run 已投递终态",命中则 `stale_terminal_discarded`(cancelled),不再补发旧回答;supersede 窗口上限由 5s 放宽到 60s。
- **媒体承诺闸门**:视频进度消息移到 provider 返回真实任务 ID 之后(`media_task_created` 阶段);进度消息每 run 去重;无任务证据的终态"马上给你看"类文本在 outbox 单一咽喉点被改写为诚实短句;种子样本与世界书新增 `doubao-task-honesty` 承诺纪律,v69 迁移同步修正存量库。
- **失败诚实**:进度承诺已发出的 run 失败时必须发声,不允许静默;未被明确叫到(非 @/回复/唤醒/直接续聊/命令)的失败一律静默。403/401 归类凭据故障,同凭据不再回退,最多尝试两个凭据对;所有模型调用链受每步墙钟预算约束(chat 12s,其余 75s),尝试超时按剩余预算裁剪。
- **观测面**:`agent_runs` 新增 `failure_class`、`first_response_ms`;`GET /api/v1/runs/stats?hours=N` 聚合状态、错误码、失败分类、参与处置和首响 P50/P95;媒体链路新增 `media_task_created/media_poll/media_download_completed/media_attached` 阶段,配合既有 `connector_send/ack_received` 构成创建→ACK 证据链。
- **人性打磨**:骨架级避重(口头禅开场重复触发重写);多段回复按 1.1s 节奏错峰投递(经 `next_attempt_at`,不阻塞投递循环);webhook 入站补齐 @/回复识别,与 gateway 对齐。
- **搜索质量**:RSS 来源逐条相关性过滤,无相关来源时诚实短失败,不再让无关来源进摘要。

## 尚未完成

- 小满个人 QQ 侧车登录和真实账号 canary(NapCat 于 2026-08-14 真实在线收发过,后被腾讯踢下线,重扫码即可恢复)。
- 多个 QQ Official BotGo WebSocket 在同进程内的真实并行验收。
- `telegram_user` MTProto 的生产资源评估和真实账号 canary。
- 除当前 QQ 外的其他平台真实账号矩阵验收。
- 长时间群聊下的主动参与命中率复盘(需一周真实群样本;P95 统计面已具备)。
- 图片下载阶段的独立 stage(当前由 `media_attached` + ACK 覆盖);`SmartMaxBatchSize` 仍未接线(同发送者连发合并现只覆盖 ping 类)。
