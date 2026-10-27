# Prof.C 持续事项记忆 MVP

把已指定事项的聊天、文档和会议产物变成可追溯的私有历史。SQLite 是事实账本；飞书文档、Markdown 脉络和对话上下文都是派生视图。采集成功只表示读到资料，来源中的“上线”“测试通过”仍是来源陈述。

## 数据怎样流动

1. `collect` 通过现有 `lark-cli --as user` 读取白名单来源，保存确切链接、来源版本、发生时间、采集时间及实际发送人。文档没有可证实的发生时间时保留未知。
2. 聊天只有精确项目词、文档 token 或任务 ID 等强锚点才正式关联。群成员身份不等于事项相关性；无锚点资料保留在本地，不进入模型和正式上下文。文档需配置明确的事项关联。
3. `classify` 调用临时、关闭工具与用户配置的 `codex exec`，从完整原文片段中选关键事实。程序重新核对引用、人物、时间、事项及替代关系，失败保留 pending 并退避。
4. 事件只追加。新事实不会仅因采集较晚覆盖旧事实；只有同事项、同阶段、同维度、明确引用旧事实的纠正才建立替代边。旧版本晚到不能替代新版本。
5. `render` 写出每事项的 `timeline.md` 与最多 3200 字符的 `context.json`。上下文按类别保留近期证据，明确注明节选及完整历史路径。历史没有被节选策略删除。
6. 可选 `publish` 将同一账本投影到固定私有飞书文档。先验证当前用户及文档权限；外链关闭、外部访问关闭，协作者只能是该用户。写入使用远端 revision；人工修改产生漂移后停止自动覆盖。创建结果未知不重建，写入结果未知先只读验收。

这不是全账号自动搜索。MVP 仅处理配置来源和窗口；会议当前通过明确绑定的会议产物文档读取，不会自动入会或监听全部会议。新增群、私聊、会议文档和完全没有强锚点的短回复需要补充来源或明确关联。读取能力仍取决于现有身份权限。

## 运行

需要 Python 3.10+、已登录的 `lark-cli`、可运行的 `codex`。仅使用 Python 标准库。

状态目录须为当前 OS 用户所有、权限 `0700`；`config.json` 为 `0600`。真实消息、凭据、运行态和配置不要放进 Git。

```sh
PYTHONPATH=tools/work_memory PYTHONDONTWRITEBYTECODE=1 \
  python3 -m profc_memory --state-dir "$PRIVATE_STATE" run

# 只读状态及本人上下文
PYTHONPATH=tools/work_memory python3 -m profc_memory --state-dir "$PRIVATE_STATE" status
PYTHONPATH=tools/work_memory python3 -m profc_memory --state-dir "$PRIVATE_STATE" \
  context --work-id byok --requester-id "$RUNTIME_OWNER_ID" --audience owner
```

`collect`、`classify`、`render`、`publish` 可以单独执行。`run` 顺序采集、提取、渲染；仅在 `publish.enabled=true` 时更新远端页。全流程使用非阻塞文件锁，已有运行时拒绝重叠。一个来源失败不阻止其他来源；部分分页失败不会推进该来源游标。每次默认最多 1 批、每批最多 8 个来源，单批提取限时 120 秒；错误退避 1–24 小时，不做无限重试。

配置示例见 [config.example.json](config.example.json)。两个 owner ID 属于不同应用的身份空间，不能按字符串相等推断是否同一人；启用发布前应实际核验映射。`max_classification_batches` 最大为 5。

消息来源可设 `reconcile_days=30`，每次重查近 30 天且不早于 `start`，只提取新增或改变的来源版本。这能发现窗口内旧消息编辑，但不能保证发现退出关键词过滤的编辑、窗口外编辑或所有旧话题的新回复。CLI 仅给分钟精度时，来源版本加入内容 hash，同一分钟内的多个版本保持不可排序。`collection_status.json` 记录每来源最近覆盖窗口和错误；“无新增”不等于“全部世界没有变化”。

## 对话接入

daemon 使用自己的 `StateDir/work-memory/manifest.json`，默认未配置时不注入。只有原始用户输入、实际 queue actor 等于 owner、消息 ID 一致、完整 surface 已配置且非 limited/helper/自动续办时才注入。每个获准的 `prompt.send` 重新读取，因此新 thread 和压缩后的下一轮不依赖旧对话摘要。

```json
{
  "schema_version": 1,
  "enabled": true,
  "owner_user_id": "RUNTIME_APP_SCOPED_OWNER",
  "bindings": [
    {"surface_id": "EXACT_VERIFIED_PRIVATE_SURFACE", "active_work_ids": ["byok"]}
  ],
  "works": {"byok": {"context_path": "works/byok/context.json"}}
}
```

绑定的是完整 surface，不能把一个私有话题的绑定扩展为整个群或其他人的会话。单次本地 I/O 等待有界，资料坏文件或读取失败会跳过注入；已知私有历史的归属无法核验时拒绝复用。私有 thread registry 和 collector 目录外的启用标记用于阻止丢文件后被误当成普通历史。新 thread 的关联由原生 request 的精确 CommandID 完成，不猜测当前 surface 的 pending 任务。

注入块按不可信来源资料处理，不增加执行权限。debug raw 日志会脱敏带记忆标记的输入；这不代表模型派生回答或已有本机历史完全不留痕。该机制服务于同一可信 OS 用户的本人环境，不能代替 OS/容器隔离。

## 定时与停用

试点可用单个 macOS LaunchAgent 每 1800 秒执行一次 `run`，`RunAtLoad=false`、无 `KeepAlive`，指向固定安装的 Python 包；标准输出仅存安全计数、版本、页 URL 和错误类型。先人工跑通一次并核对账本、页面和覆盖状态，再加载定时器。

停采集：卸载对应 LaunchAgent。停远端更新：将配置 `publish.enabled` 设为 `false`。停新注入：将 manifest 的 `enabled` 设为 `false`。**不要删除 `private-threads.json` 或 `work-memory-privacy-enabled.json` 来停用**，关闭注入不应解除已有私有历史的保护。

飞书是阅读视图；修正事实应追加带来源的纠正事件，不能直接改数据库旧行。MVP 没有 GBrain 写入、Obsidian 同步、自动业务执行或群通知。

## 验证

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=tools/work_memory \
  python3 -m unittest discover -s tools/work_memory/tests -q
go test ./internal/app/daemon ./internal/adapter/codex ./internal/debuglog
```

测试覆盖不可变证据、乱序版本、阶段关系、完整否定引用、密钥清洗、跨事项隔离、所有者检查、权限漂移、未知创建/写入恢复、冷启动/压缩后刷新及核心 lifecycle 不被可选记忆 I/O 吞掉。真实业务验收需另外核对实际来源、读回的固定页和实际部署版本。
