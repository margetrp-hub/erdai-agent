# 二呆智能体系统说明

> 文档版本：0.12.13（Schema v75）
>
> 公开文档只记录源码、接口和可复现的验收方法。生产主机、网络地址、备份路径和真实账号验收证据保存在私有运维记录中。
>
> 本文只描述项目当前源码、数据库和线上运行事实。没有真实账号或真实 canary 的能力会单独标注，不把目录、表单或协议测试写成生产可用。

## 1. 项目定位

二呆智能体是一个纯 Go 的通用智能体运行平台。

- 豆包只是第一张角色卡，不是 Core 中写死的唯一智能体。
- QQ 只是一个连接器；同一个 Core 可以接入多个平台和多个角色。
- AstrBot 的运行能力已经按能力迁移到 Go，不再启动 Python/AstrBot 运行层。
- WebUI、配置、模型、记忆、知识、工具、MCP、任务和投递都由 Core 统一管理。
- 供应商密钥只从服务器环境变量读取，不进入 SQLite、浏览器响应、日志和发布包。

## 2. 当前运行拓扑

```text
平台连接器
  -> Transport Event v2 归一化
  -> Core 所有权判断（off / shadow / active）
  -> 线程、关系、记忆、RAG、角色上下文
  -> 场景路由（chat / reasoning / vision / document / search / image / video / tools）
  -> 模型与工具循环
  -> 统一回复质检、分段和附件整理
  -> Core Outbox
  -> 连接器 lease -> 发送 -> ACK / FAIL / retry
```

生产业务逻辑只有一个 Go 进程；发布包同时带有本地向量服务和仅内网可访问的监控截图浏览器：

| 监听 | 用途 | 认证 |
| --- | --- | --- |
| `6280` | Runtime、Transport、健康检查 | Runtime Bearer Token |
| `6282` | 管理 API 和 WebUI | 管理员会话或管理 Token |

持久化使用 SQLite WAL。运行库和配置库默认使用同一数据库，媒体文件位于数据库旁的 `data/media` 目录。

当前线上容器：

- Core：`erdai-agent:0.9.4-runtime-instances-r2`，healthy。
- Embedding：`erdai-embedding`，healthy。
- QQ 通道是否 `active` 以线上 Core 的 `channel_runtime` 配置为准，不以旧文档为准。
- 旧镜像、回滚容器和部署备份单独保留，不参与当前请求处理。

## 3. 一条消息的完整生命周期

### 3.1 入站

连接器把平台原始事件转换为统一事件，保留：

- `eventId`、`messageId`、`replyToMessageId`、`threadKey`
- 平台、实例、会话和会话类型（群聊或私聊）
- 发送者引用、显示名、提及列表和回复对象
- 文本、附件列表、时间和唤醒标记
- 连接器可信管理员身份

重复事件由 `eventId` 和幂等键拦截。连接器不能通过消息体伪造 `isAdmin`。

### 3.2 所有权判断

- `off`：Core 拒绝接管。
- `shadow`：Core 只观察、记录判断，不生成 Run 和 Outbox。
- `active`：满足唤醒、续聊、命令、附件续接或主动参与评分时返回 `owned`。

群聊会先判断：是否 @、是否回复豆包、是否明确续聊、是否是命令、是否与当前线程相关、最近是否已经回复过、是否属于低价值表情或无关消息。

### 3.3 运行与投递

接管后会：

1. 建立持久 Run。
2. 持久化入站附件引用和必要的加密上下文。
3. 载入当前角色、世界书、样本、关系、近期消息、长期记忆和知识命中。
4. 按场景选择模型端点。
5. 执行一次或多次模型调用，必要时调用工具、MCP 或媒体任务。
6. 统一做身份暴露、客服腔、重复开场、残句和分段检查。
7. 将进度和终态写入 Outbox。
8. 连接器租约发送，收到 ACK 后标记 delivered；失败按策略重试或记录失败原因。

Run 的阶段时间线会记录事件接收、上下文、路由、模型、首字、质检、Outbox、发送和 ACK 耗时。

## 4. 多角色系统

### 4.1 角色卡是什么

角色卡是运行配置的集合，不只是系统提示词。每张卡可以独立配置：

- 名称、简介、性格、场景、开场白、边界和世界观。
- 系统提示词、特质、场景样本、禁用表达和候选短回复。
- 世界书条目、关键词、优先级和注入条件。
- 聊天模型、任务模型、判断模型和工具白名单/黑名单。
- 主动参与、最大字数、最大句数、记忆策略和表达提示。
- 头像、外形描述、视觉提示词、自拍类型和视觉参考附件。

豆包的 QQ 昵称只是连接器显示名。运行时通过 `personaId` 绑定角色，不把“豆包”硬编码到所有流程。

### 4.2 角色绑定与切换

角色可以按全局、平台、群或私聊绑定。高优先级绑定覆盖低优先级绑定。

QQ 命令：

```text
/角色
/角色列表
/切换角色 角色名
```

普通成员只读；可信管理员才能切换。切换记录旧角色、新角色、操作者、来源和时间，支持回滚。切换不会删除旧角色记忆，切回后恢复原关系和表达连续性。

### 4.3 角色视觉资料

每张角色卡可以上传并整理头像、半身、全身、自拍、动作和视频参考。视觉参考支持：

- 单独预览、启用/停用、设为主参考。
- 导入/导出 `.erdai.zip` 包。
- 服务端事务导入，任一子项失败则整包回滚。
- 参考图片和视频只作为生图提示和视觉一致性约束，不自动识别或伪造真实身份。

## 5. 对话与拟人化

### 5.1 社交动作先于文字

Core 先决定社交动作，再生成文本：

- 接话
- 接梗
- 轻怼
- 安慰
- 办事
- 澄清
- 保持安静

普通群消息不是“看见就回答”。主动插话使用话题增量、关系熟悉度、气氛、最近回复密度、是否有人已经回答和角色主动策略综合评分。

被 @、被回复和明确续聊属于稳定接话路径；重复 @ 会去重，跨线程消息不会强行接入。

### 5.2 回复表达约束

- 普通闲聊默认 1 至 2 段。
- 每段按当前角色的字数配置生成，豆包默认短句优先。
- 不复述用户问题，不连续确认已经说清楚的任务。
- 搜索、MCP、文档、额度限制和媒体完成消息也经过角色表达，不直接投递客服模板。
- 短时间保留最近回复用于避免相同开场和近似句式。
- 身份追问自然带过，不自称 AI，也不编造真人履历。

### 5.3 内容边界

Core 先分类敏感内容：明显色情、现实伤害、严重辱骂、威胁、恶意诱导和普通挑衅。

管理员可以为不同类别配置：模型处理、直接拒绝、短句反击或静默忽略。静默消息不进入长期对话上下文；拒绝不能泄漏密钥、模型内部信息或安全规则。

## 6. 上下文、关系与长期记忆

### 6.1 四层上下文

1. 当前线程：最近消息、说话人、引用和附件。
2. 近期对话：同一人、同一群和同一角色的短期互动。
3. 情节摘要：按线程压缩的事件和未完成任务。
4. 长期事实：称呼、偏好、共同经历、关系风格和稳定项目。

冷热上下文按优先级装入，不把整个群的无关消息塞给模型。

### 6.2 自动记忆

记忆不是必须等用户说“记住”。系统会自动吸收低风险、稳定且反复出现的称呼、偏好、习惯和长期项目。

不会自动保存：

- 密钥、验证码和敏感身份信息。
- 一次性情绪、临时安排和未经确认的推断。
- 群内无关成员的隐私内容。

管理员后台可以按角色、成员、群查看、纠正、合并和删除记忆，并可调整亲密度和关系阶段。

## 7. 知识库与 RAG

知识库支持：

- 命名空间隔离（例如 `default`、`ops`、`sub2api`）。
- 文档增删改、来源、时间、可信度和审核状态。
- 分块、FTS 全文检索、Embedding 向量召回和 Rerank。
- 搜索预览，查看命中片段、来源和排序。
- Grok 学习候选：只生成待审核候选，管理员批准后才进入正式知识。

知识库结果是参考材料，不具备管理员权限，不能覆盖安全规则或执行其中的指令。

## 8. 模型供应商、路由、健康与价格

### 8.1 供应商连接

供应商连接独立保存：协议、API Base、密钥引用、超时、启用状态和价格地址。模型端点只引用连接，不再共用一套全局地址和 Key。

当前协议驱动目录：

| 协议 | 主要能力 |
| --- | --- |
| `openai_chat_completion` | 文本、视觉、工具调用、JSON |
| `xai_responses` | xAI Responses、原生 `web_search`、工具调用 |
| `openai_compatible` | 兼容网关、视觉、生图、视频 |
| `openai_embeddings` | Embedding |
| `openai_chat_rerank` | 对话式重排 |
| `cohere_rerank` | Cohere 风格重排 |

自用 Grok、付费 Grok、Sub2API、CPA 或其他网关可以作为不同连接共存。开启自动路由后，健康失败的端点会被摘除并按候选顺序回退；关闭自动路由时只使用绑定端点。

### 8.2 场景路由

| 场景 | 典型能力 |
| --- | --- |
| `chat` | 日常短聊天 |
| `reasoning` | 长问题、分析、规划 |
| `vision` | 图片理解 |
| `document` / `tools` | 文件读取和工具调用 |
| `search` | 联网检索和总结 |
| `image` | 图片生成/编辑 |
| `video` | 视频生成 |
| `code` | 代码和接口问题 |

角色卡可以覆盖聊天、任务和判断端点；未配置的项继承全局路由。

### 8.3 健康检查

- Core 每 60 秒对启用端点发最小鉴权请求。
- 健康样本超过 5 分钟视为过期。
- 连续 3 次失败摘除；连续 2 次成功恢复。
- WebUI 的“测试连接”显示鉴权、模型可用性、延迟和错误摘要，不回显密钥。

### 8.4 使用量和价格

每次模型调用记录供应商、模型、端点、Run、输入 Token、输出 Token、总 Token、单价、估算费用和时间。

价格可以通过供应商价格事实源同步，也可以人工维护。未配置价格时显示“无价格事实”，不把 0 当成免费。WebUI 按连接、供应商、端点和时间汇总调用次数、Token、估算费用和最近使用时间。

## 9. 搜索与联网总结

### 9.1 触发规则

只在以下情况调用搜索：

- 用户明确说搜索、查询、联网、最新、新闻、来源或官网。
- 问题明显依赖实时信息。
- 当前知识不足且搜索策略判断值得查。
- 用户要求出处或链接。

普通闲聊、无关图片、已经有明确答案的问题不搜索。

### 9.2 搜索链路

```text
意图判断 -> 一次 Web Search -> 相关性校验 -> Grok 归纳 -> 角色短回复
```

同一 Run 最多一次搜索；重复工具调用复用第一次结果。默认只发简短结论，不把搜索结果列表原样丢进群里；用户要求来源时最多给必要链接。

搜索实体按角色、会话、线程、发送者隔离，旧实体不会覆盖当前图片或附件上下文。

## 10. 附件与媒体

### 10.1 支持的入站附件

Core 接受并持久化四类附件：

- `image`：图片、截图、自拍、表情图。
- `file`：PDF、Word、Excel、PPT、Markdown、CSV 和其他文档。
- `audio`：语音、录音、音频。
- `video`：视频、短片、录像。

附件保存消息 ID、发送者、会话、线程、媒体类型、文件名、来源和过期时间；敏感引用进入加密字段。最近附件按会话保留，重启后仍可恢复。

### 10.2 附件续接

如果豆包上一条明确要求“把文件/语音/视频/图片发来”，同一发送者在同一线程补发附件时，Core 会恢复上一轮任务意图，绕过普通群聊随机门禁。

### 10.3 内容处理边界

- 图片进入视觉模型和图片编辑链路。
- 文件进入 `read_document`，可读取 Word、Excel、PPT、PDF 和文本。
- 音频、视频目前保证持久化、关联、续接和投递；内容级语音转写、视频逐帧理解需要配置对应的 STT/视频理解 Provider，当前 Core 不虚报为已完成。

### 10.4 生图、生视频和清理

- 图片和视频使用独立并发池，不阻塞普通聊天。
- 每人每日图片/视频配额可配置。
- 管理员和白名单可以不受配额限制。
- 生成任务记录步骤、产物、失败原因和投递状态。
- 媒体 GC 支持 dry-run、TTL、运行中任务保护、待投递保护、角色视觉参考保护、删除数量和释放空间指标。

## 11. 工具、技能和 MCP

### 11.1 Core 工具

工具注册包含名称、适配器、能力、风险等级、权限、审批模式、超时和输入 Schema。当前内置方向包括：

- 图片生成、图片编辑、视频生成。
- Grok Web Search。
- OPS 分组状态查询。
- 文档读取和 Office 文档生成。
- 长期记忆写入、召回、纠正和删除。
- 角色视觉预设查询。

### 11.2 技能目录

技能是可搜索、可启用的行为模块。每条技能包含触发词、附件类型、所需工具、所需 MCP、允许角色和优先级。搜索后加载只加载当前场景需要的技能，不把全部说明塞进每次上下文。

### 11.3 MCP

Core 支持：

- Streamable HTTP / HTTP MCP。
- Legacy SSE MCP。
- 受控 stdio MCP。

MCP 调用有启用状态、工具白名单、成员/管理员权限、审批模式、超时、响应大小上限和审计记录。stdio 命令必须命中服务器环境中的显式 allowlist；外部结果视为不可信材料，不能提升权限。

## 12. 持久任务图

复杂任务不会只依赖内存循环。每次模型、工具、媒体和投递步骤都可以写入任务图：

- 状态：pending、running、succeeded、failed、cancelled。
- 重试次数、错误码、开始/结束时间和父步骤。
- 工具输出和媒体产物加密保存。
- 重启后可以恢复 queued/running 任务，已成功步骤可复用，避免重复收费。
- 后台可以查看 Run、步骤、产物、错误和恢复操作。

普通聊天保持一次模型调用；只有工具、搜索、文档、图片和视频任务才进入更长的执行链。

## 13. OPS 分组状态

OPS 查询只能通过显式命令触发，例如：

```text
/渠道
/分组名
/雷达
```

`/渠道` 登录专用的零余额普通用户，直接截取 Sub2API 渠道监控页面固定 `90m` 视图并发送图片。截图固定中文，只在临时截图页隐藏公告遮罩，不触发“标记已读”；页面原始卡片负责展示近 90 分钟分组状态、可用率、倍率和 5 分钟时间桶，Core 不再自行推断红黄绿。截图浏览器只接入内部 Docker 网络，不发布端口；账号密码只保存在受管凭据文件中。单组查询继续返回对应分组详情，雷达数据继续使用配置的 CodexRadar 地址或其他管理员指定事实源。

## 14. 19 个原生 Go 连接器

当前连接器目录：

1. QQ Official
2. QQ Official Webhook
3. OneBot v11 / aiocqhttp
4. Telegram
5. Telegram User / MTProto（源码保留，当前停用并延后验收）
6. Discord
7. KOOK
8. Mattermost
9. Misskey
10. Satori
11. LINE
12. WeCom
13. Weixin Official Account
14. Slack
15. WebChat
16. Lark / Feishu
17. DingTalk
18. WeCom AI Bot
19. Weixin Personal / iLink

每个连接器都通过同一套 Core 处理：认证、重连、入站归一化、文本和媒体发送、Outbox ACK/FAIL、健康状态和管理员身份。

“源码有连接器”不等于“真实账号已验收”。除 QQ 外，其余平台是否生产可用仍以真实凭据、入站、文本、媒体、重连和 Outbox canary 为准。

## 15. WebUI 管理后台

WebUI 与 Core 同源提供，左侧导航按模块聚合，避免把所有字段堆在一页。

主要模块：

| 页面 | 可管理内容 |
| --- | --- |
| 总览 | Core、Embedding、连接器、模型、Run、Outbox、健康和资源状态 |
| 运行中心 | 运行实例、策略模板、角色卡、连接器账号、会话路由和最终有效配置 |
| 智能体/角色卡 | 创建、编辑、复制、删除、导入、导出、切换和绑定 |
| 角色运行档案 | 模型覆盖、工具权限、主动参与、回复长度、记忆和视觉提示 |
| 记忆与关系 | 成员记忆、亲密度、关系阶段、纠正、合并和删除 |
| 世界书/人格样本 | 关键词、场景样本、特质、禁用表达和权重 |
| 模型供应商 | 供应商连接、协议、端点、密钥引用、测试、价格、健康和使用量 |
| 路由 | 场景线路、自动/手动、锁定端点、回退顺序和决策解释 |
| 平台连接器 | 19 类平台、实例、开关、凭据引用、平台设置和运行状态；MTProto 当前停用 |
| 工具与技能 | 工具注册、风险、权限、审批、技能搜索后加载 |
| MCP | HTTP、SSE、stdio 服务、发现、白名单、调用和审计 |
| 知识库/RAG | 文档、分块、命名空间、Embedding、搜索预览、候选审核 |
| 任务与审计 | Run 时间线、任务步骤、产物、搜索调用、模型选择、投递和失败原因 |
| 媒体与配额 | 图片/视频配额、白名单、媒体 TTL、dry-run 和 GC 报告 |
| 系统设置 | 群聊策略、消息策略、内容边界、管理员指令、OPS 和运行参数 |

字段只有在运行时有消费路径时才应保留。JSON 编辑区适合高级配置，常用设置应通过表单、弹窗和测试按钮完成。

## 16. 关键管理 API

### 系统与运行

```text
GET    /healthz
GET    /api/v1/overview
GET    /api/v1/audit
GET    /api/v1/runs
GET    /api/v1/runs/:id
POST   /api/v1/runs/:id/cancel
GET    /api/v1/tasks
GET    /api/v1/tasks/:id
POST   /api/v1/tasks/:id/retry
POST   /api/v1/tasks/:id/cancel
POST   /api/v1/maintenance/media-gc
```

### 模型与供应商

```text
GET/POST/PUT/DELETE /api/v1/provider-connections
POST   /api/v1/provider-connections/:id/test
POST   /api/v1/provider-connections/:id/pricing-sync
GET    /api/v1/provider-drivers
GET/PUT/DELETE /api/v1/model-endpoints/:id
GET    /api/v1/model-health
GET    /api/v1/model-health/:id/history
PUT    /api/v1/model-health/:id
GET/PUT /api/v1/routing/control
GET/PUT /api/v1/routing/lanes
POST   /api/v1/routing/simulate
```

### 角色、记忆与知识

```text
GET/POST/PUT/DELETE /api/v1/personas
GET/POST/PUT/DELETE /api/v1/personas/:id/worldbook
GET/POST/PUT/DELETE /api/v1/personas/:id/samples
GET/POST/PUT/DELETE /api/v1/personas/:id/traits
GET/PUT /api/v1/personas/runtime-profiles
GET/POST/PUT/DELETE /api/v1/persona-bindings
GET/POST/PUT/DELETE /api/v1/personas/:id/visual-references
GET    /api/v1/personas/:id/visual-references/export
POST   /api/v1/personas/:id/visual-references/import
GET/POST/PUT/DELETE /api/v1/memories
GET/PUT/DELETE /api/v1/relationships
GET/POST/PUT/DELETE /api/v1/knowledge/documents
POST   /api/v1/knowledge/search-preview
GET/POST/PUT/DELETE /api/v1/runtime/knowledge-candidates
POST   /api/v1/runtime/knowledge-candidates/:id/review
GET/PUT/DELETE /api/v1/runtime/directives
```

### 平台、工具与 MCP

```text
GET    /api/v1/platforms/catalog
GET/POST/PUT/DELETE /api/v1/platforms
GET    /api/v1/platforms/runtime-status
GET    /api/v1/platforms/:id/login-qr
GET/POST/PUT/DELETE /api/v1/tools
GET/POST/PUT/DELETE /api/v1/skills
GET/POST/PUT/DELETE /api/v1/mcp/servers
POST   /api/v1/mcp/servers/:id/discover
POST   /api/v1/mcp/servers/:id/call
GET/PUT /api/v1/integrations/:id
```

### 连接器运行接口

```text
POST /api/v1/runtime/prepare
POST /api/v1/transport/events
POST /api/v1/transport/deliveries/lease
POST /api/v1/transport/deliveries/:id/ack
POST /api/v1/transport/deliveries/:id/fail
GET/PUT/DELETE /api/v1/runtime/media-quotas
```

管理 API 需要管理员会话或管理 Token；Runtime API 只接受 Runtime Token。接口不会回显密钥。

## 17. 数据与隐私

### 配置数据

角色、世界书、知识、技能、工具、MCP、模型端点、供应商连接引用、平台配置、路由和策略存入 Core 配置库。

### 运行数据

Run 输入、附件引用、工具输出、搜索结果、任务产物和投递状态存入运行库；敏感正文和附件引用使用加密字段。

### 不进入数据库的内容

- Provider API Key、QQ Secret、Runtime Token 和加密主密钥只来自环境变量。
- 日志和管理响应不得输出密钥。
- 对外文档、知识区和交付包不得写入真实密钥。

## 18. 部署与维护

### 服务器构建原则

当前项目约定在 OVH 二呆主节点的 Docker 内构建、测试、验收和发布，不在本机或旧 250 运行生产 Go 构建。

```text
备份 SQLite 与 Compose
  -> 构建 verify 镜像
  -> go test ./...
  -> go vet ./...
  -> 构建正式镜像
  -> off
  -> shadow 回放
  -> 单群 active canary
  -> 观察 72 小时
  -> 批准正式切换
```

### 常用检查

```bash
docker ps
curl -fsS http://127.0.0.1:6282/healthz
docker exec erdai-agent /app/erdai-agent --health-check
```

生产改动必须保留：

- 发布前数据库备份。
- 当前镜像摘要和 Compose 配置。
- 回滚镜像与回滚容器。
- canary 的 Run、Outbox、连接器发送和 ACK 证据。

## 19. 测试覆盖

源码测试覆盖：

- Core Schema、迁移、统一数据库和 SQLite 完整性。
- 19 类连接器的协议归一化、身份、文本、媒体、重连和投递。
- QQ 群 @、回复续聊、普通消息观察、主动插话、去重和管理员权限。
- 角色卡、绑定、运行档案、世界书、样本、特质和视觉参考导入导出。
- RAG FTS、Embedding、混合召回、Rerank 和搜索持久化。
- 供应商连接、健康摘除/恢复、价格同步和 Token/费用统计。
- 工具权限、审批、MCP HTTP/SSE/stdio、任务步骤、重试和取消。
- 图片/视频配额、附件持久化、Outbox ACK/FAIL 和媒体 GC。
- 回复分段、近似重复、身份暴露、敏感边界和自然度守门。

当前线上最近一轮 r53b 验证：

- `go test ./...`：通过。
- `go vet ./...`：通过。
- 附件续接回归：通过。
- Core 与 Embedding 健康：通过。

## 20. 当前已完成与未完成边界

### 已完成或已进入生产链路

- 纯 Go 单进程 Core、WebUI 和连接器管理。
- 19 类连接器源码实现和统一 Outbox；源码存在不代表真实账号已验收。
- QQ 主链路、@、回复续聊、去重和附件续接。
- 多角色卡、绑定、切换、记忆隔离和视觉参考管理。
- 独立 Provider、模型路由、健康检查、价格和使用量统计。
- Grok 搜索一次调用限制、相关性检查和短总结。
- 图片生成、图片编辑、视频生成任务链和配额。
- 文档读取、Office 生成、OPS 显式命令、记忆和 MCP。
- 分层记忆、关系亲密度、知识候选审核、任务图、Outbox 和媒体 GC。

### 仍需真实账号或专门模型才能声明完成

- 除当前 QQ 外其他平台的生产账号 canary；Telegram User / MTProto 当前停用。
- 语音转写和语音合成的完整 Provider 链路。
- 入站视频逐帧理解和视频内容问答。
- 桌面数字人、语音唤醒、3D 形象和 AIRI 类前端运行时。
- 大规模群聊长时间观察下的主动插话概率和 P95 统计。

这些项目已经有数据结构或扩展位置时，文档仍标记为“待真实验收”，不能用 WebUI 有入口或协议测试通过替代生产可用。

## 21. 排障顺序

1. 先看 `6282/healthz`、容器状态和 Core 日志。
2. 再查 `/api/v1/runs/:id` 的阶段时间线、路由端点和错误码。
3. 看 Outbox 是否创建、是否 lease、是否 ACK/FAIL。
4. 若模型未调用，检查群聊所有权、角色绑定、工具权限和内容边界。
5. 若模型慢，查看供应商健康、端点回退、首字和工具步骤耗时。
6. 若附件失败，检查附件持久化、媒体目录、文件类型策略、任务产物和发送记录。
7. 若搜索不准，检查搜索触发、调用次数、相关性校验、实体隔离和摘要结果。
8. 任何配置异常都要确认该字段是否有运行时消费路径，而不是只看页面是否显示。

## 22. 设计原则

- Core 是唯一配置源，WebUI 只是 Core 的编辑和观察界面。
- 角色是数据，不是写死的分支。
- 工具和 MCP 默认最小权限，敏感操作必须审批和审计。
- 外部搜索、网页、知识库和工具结果都是不可信输入。
- 群聊优先判断是否值得回应，允许安静，不追求消息数量。
- 回复先确定社交动作，再生成短句；任务结果必须完整，闲聊必须克制。
- 所有关键行为都要有持久状态、可解释理由、可回放证据和回滚路径。
