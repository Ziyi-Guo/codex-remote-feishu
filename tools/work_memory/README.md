# Prof.C 持续事项记忆

人和 agent 共同使用一份有版本的项目档案。首页先写来源明确的当前目标与完成条件，再回答现状、背景、问题、决策和近期关注；详细取舍放在 decisions/，来源页提供人物、日期、标题和原始链接。原始资料按版本留存在本机证据库，整理后的 Markdown 可以直接作为 Obsidian 正本。updates.md 保存每次整理的具体变化，供工作摘要和项目回顾使用；更新记录不表示业务验收。

## 档案与更新

`run` 依次采集限定来源、提取来源陈述、渲染诊断视图，以及整理已启用的项目。来源版本变化才重新整理，每轮最多尝试一个实际待更新项目，跳过未变更和退避的项目，避免第二个项目一直被跳过；失败退避一小时。首版注明根据现有资料重建，不能冒充过去某天已经形成的判断。

来源库保存完整的已清洗原文、版本和链接。events 是供整理和审计使用的来源陈述，不是另一份独立维护的决策正本。新增资料只有强锚点才能永久归并，群成员身份不等于相关性。提议、部署、测试和业务验收保持各自含义。

每个项目目录包含 home.md、decisions/*.md、sources.md、manifest.json 和 versions/<版本>/ 快照。段落引用必须匹配事项库中的真实来源和版本，链接由程序生成，原始聊天不搬入 Obsidian。程序核实引用与安全边界，摘要是否保留原意仍需语义验收。可证明较旧的来源版本不能作为首页现状或有效决策栏的依据。

页面末尾“人工补充”区在重编译时保留。机器区被手改会返回 drift 并暂停覆盖。原文标识只放审计元数据；读取正文时去除 HTML 注释。版本快照保留当时页面与构建原因。

## 按需读取

需要 Python 3.11+、已登录的 lark-cli 和 codex，业务模块仅用标准库。状态目录0700，config与文件0600。真实身份、原始数据和认证信息不进Git。

```sh
PYTHONPATH=tools/work_memory PYTHONDONTWRITEBYTECODE=1 \
  python3 -m profc_memory --state-dir "$PRIVATE_STATE" recall \
  --requester-id "$RUNTIME_OWNER_ID" --query 'BYOK'

PYTHONPATH=tools/work_memory PYTHONDONTWRITEBYTECODE=1 \
  python3 -m profc_memory --state-dir "$PRIVATE_STATE" recall \
  --requester-id "$RUNTIME_OWNER_ID" --work-id byok --section 现状 \
  --session-id "$NATIVE_THREAD_ID" --max-chars 1600
```

query只返回项目候选、一句话简介和更新时间，不读项目正文。名字未命中返回catalog_fallback和至多20条简短目录，由agent按语义确认事项；不能把未命中当成没有历史。确认事项后再读相关栏目，问下一步同时读“目标”和“问题”，必要时使用 `--section decision:<slug>` 或 sources；`source:<依据序号>`读取本机保存的确切原文版本，history读取项目更新及档案修订历史。无关问题不发起读取；询问进展、可用性或完成度，应同时核对“现状”和“问题”。

相同session/事项/栏目/版本/正文不重复返回。切换事项、版本或人工补充变化会刷新；压缩导致相关正文不再在上下文时传 `--after-compaction`。字符预算按完整段落装入并返回truncated，不能把省略的材料当成不存在。模型token开销需从真实调用回执另外核对。

缓存与审计回执位于state/retrieval，按requester/audience/session隔离。非owner和shared拒绝，路径遍历和符号链接拒绝。此机制面向同一可信OS用户，文件权限不能替代进程隔离。

## 配置与会话入口

示例见 [config.example.json](config.example.json)。dossiers.enabled和work_ids启用限定项目；dossiers.directory可指定绝对私有Markdown正本目录，例如选定的Obsidian子目录，默认state/dossiers。读写使用同一目录和锁，不另维护镜像结论。

协调读写的 `dossier.lock` 保存在本人本机状态目录，不放进 iCloud 正本目录；flock只协调本机进程，云端可写锁文件会使launchd任务卡在文件打开。项目文档与历史仍只保存于选定的Obsidian正本目录。

定时运行应选不经过云盘的本机 Obsidian 目录。若 iCloud 后台文件访问仍阻塞，可迁移完整档案和版本到本机目录，原目录保留迁移前归档与入口说明；不新增第二份运行正本或后台服务。当前读取只验证当前文件，历史在明确请求时读取，不能每次查首页都扫描所有旧版本。

retrieval.gbrain_candidates可使用现有认证的loopback GBrain MCP entity定位候选。只接受本地已有项目的明确标识，忽略facts/正文，失败回退本地目录。不另开PGLite、不写GBrain、不启付费embedding。远端私有正文读取尚未验证，项目正文始终从本人受保护的本机正本读取；服务可用不等于项目已被索引，空命中不等于没有历史。

第二版manifest示例：

```json
{
  "schema_version": 1,
  "mode": "on_demand",
  "enabled": true,
  "owner_user_id": "RUNTIME_APP_SCOPED_OWNER",
  "guide_path": "runtime-guide.json",
  "bindings": [{"surface_id": "EXACT_VERIFIED_PRIVATE_SURFACE"}]
}
```

固定runtime-guide.json为 `{"schema_version":1,"kind":"runtime_entry","instruction":"短入口规则"}`，最多4KiB。只放可信的发现/读取规则，不能放项目目录、正文或外部资料。入口追加到获准thread的developer instructions，保留profile规则；首次、规则变化或原生配置失效时，经精确thread/resume响应确认后再发本轮请求。不会向每条用户消息追加摘录。原owner/原surface的私有历史保护继续生效。

只在维护者明确选定的本人工作群里，on_demand可用精确 `{"gateway_id":"...","chat_id":"..."}` 替代单个surface绑定，使该群本人新话题都有入口。仍校验真实Origin和队列所有者，其他群/发言人/受限surface不启用，既有私有thread不能换入口复用。表情与撤回事件保留事件操作者，但不覆盖surface的回复所有者。

启用 `discovery: {enabled: true, max_documents_per_run: 2}` 后，从既有配置来源发现明确关联的飞书docx/wiki链接，沿原采集流程每轮最多处理两个。保留发现出处；模糊归属只进入待核实候选；不递归扩展，不自动加入群，不访问任意外站。新增资料经过原有来源版本、分类与决策检查，链接发现本身不产生有效决定。

## 范围与停用

采集只覆盖白名单。reconcile_days=30能重查窗口内变更，但不能保证发现退出关键词过滤的编辑、窗口外修改或所有旧话题新回复。会议通过明确绑定的产物读取，不自动入会；完全无锚点的材料仍需明确关联。

单个LaunchAgent可每1800秒运行一次run，无KeepAlive；流程互斥，分页失败不推进游标。run整轮限制600秒，超时返回run_timeout并释放锁，避免一次可中断文件I/O阻塞后续调度；不可中断的内核I/O仍受操作系统约束。停采集卸载定时器；停新入口将manifest.enabled设false。不要删除private-threads.json和目录外work-memory-privacy-enabled.json，停用不能解除已有私有历史保护。

旧render/context/publish保留为第一版诊断兼容接口，旧快照模式仅mode缺省时兼容。第二版不使用旧摘录作为会话背景；旧飞书发布器也不更新第二版档案。业务执行、群通知、公开发布不属于本模块。

## 验证

```sh
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH=tools/work_memory \
  python3 -m unittest discover -s tools/work_memory/tests -q
go test ./internal/app/daemon ./internal/adapter/codex ./internal/debuglog
```

程序测试覆盖来源版本、否定引用、人工区、漂移、历史恢复、外部正本、预算和隔离。真实模型验收另保存输入、工具轨迹与结果，核对新会话是否真实读取、无关问题是否零正文读取及压缩后规则是否有效；native ack和服务健康不能替代这些结果。
