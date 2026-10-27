# AgentMemory

把本机各个 AI 编码工具的对话，收进一个**本地记忆库**。

Claude Code、Codex、Cursor、OpenCode 等 20 多个工具的会话被解析成统一事件流，写进一个 SQLite 主库（唯一真源，只追加）；Markdown 归档、全文搜索、喂模型的切片、统计与视角，全部从库里派生，随时可以重建。默认只留本地，可选备份到你自己的 git 仓库。

外部 Agent（dsh、Claude Code、Codex……）可以通过**只读的 MCP 接口**直接检索这份记忆。

## 能做什么

| 能力 | 说明 |
|---|---|
| 渠道自动检测 | 启动时扫描本机，列出「可用 / 未安装」的渠道，不用手填路径 |
| 增量收集 | 只读有变化的源文件；第一次全收，之后只收新的 |
| 记忆库 | SQLite 事件流（会话 + 事件），幂等入库；解析规则升级可重放 |
| 全文检索 | 界面搜索框，中文按整词短语匹配，可按渠道 / 项目 / 时间过滤 |
| 模型切片 | 会话或检索结果导出成 NDJSON，直接喂模型 |
| 统计与视角 | 渠道 / 项目 / 时间 / 规模 / 工具；按周活跃度、星期×时段、规模分档 |
| MCP 只读接口 | 五个工具：检索、读会话、读维度、导出切片、概况；另可按需解读会话（话题 / 决策 / 待办） |
| 备份 | 默认仅本地；可选 git（Markdown 导出 + 主库转储） |
| 定时收集 | 交给 Windows 任务计划程序，程序本身不常驻 |
| 网页版插件 | 配套 Chrome / Edge 插件，把网页版 AI 对话存成 Markdown 并进库 |

## 快速开始

1. 到 Releases 下载 `AgentMemory.exe`（或自己构建，见下）。
2. 双击打开，跟着三步引导：选归档目录 → 看检测到的渠道 → 定备份方式。
3. 回首页点「增量收集」。

第一次会全量收一遍，之后都是只收新增。定时任务跑的是同一件事。

## 接给外部 Agent（MCP）

1. 桌面端 → 设置页 →「对外接口」→ 打开。下面会显示两项：命令、参数。
2. 在客户端里新增一个 `stdio` 类型的 MCP server：

| 项 | 填什么 |
|---|---|
| 命令 / command | `D:\...\AgentMemory.exe`（exe 完整路径，**不带** `-mcp`） |
| 参数 / args | `-mcp` |

Claude Code（`~/.claude.json`）：

```json
"AgentMemory": {
  "type": "stdio",
  "command": "D:\\path\\to\\AgentMemory.exe",
  "args": ["-mcp"]
}
```

Codex（`~/.codex/config.toml`）：

```toml
[mcp_servers.AgentMemory]
command = 'D:\path\to\AgentMemory.exe'
args = ['-mcp']
```

**常见错误**：把 `-mcp` 一起写进「命令」框。客户端会把整串当文件名，报 `No such file or directory`。用 ccs（CC Switch）这类管理器时，命令与参数是两个框，别填在同一栏；只有单个命令框时，写一个 `AgentMemory-mcp.cmd`（内容 `"<exe 完整路径>" -mcp`）指过去。

接上之后可用：`memory_search`、`memory_get_session`、`memory_facets`、`memory_export_ndjson`、`memory_stats`；打开「视角（LLM）」并配好 `AGENTMEMORY_LLM_API_KEY` 后，还能用 `memory_interpret` 按需解读会话（话题 / 决策 / 待办，结果缓存）。

接口**只读**：不改记忆库、不碰各工具的原始会话文件、不能触发收集。设置里关掉开关，客户端就拉不起来。

## 数据与隐私

- 配置、主库、日志：`%APPDATA%\AgentMemory`（`AGENTMEMORY_HOME` 可换根目录）；主库是其中的 `memory.db`。
- 归档 Markdown：首次引导里选的目录，默认 `%USERPROFILE%\Documents\AgentMemory`。
- 程序只读各工具自己的会话目录，不修改任何原始数据。
- 默认不联网：只有备份（选了 git）和显式开启的模型调用才会发出请求。

## 备份

默认「仅本地」——不碰网络、不动别人的账号。选 git 时，备份对象是 Markdown 导出与主库转储；备份失败不丢本地数据，下一轮补做。

## 网页版捕获插件

`extension/` 是配套插件，把网页版 AI 对话批量保存成 Markdown，写到归档目录的 `网页版/<站点>/`，桌面端按「桌面端 / 网页版」分开统计。安装与支持的站点见 [`extension/README.md`](extension/README.md)。

## 开发

```bash
# 跑测试（GOTMPDIR 挪到项目里，避开杀软对新编译测试二进制的误报）
export GOTMPDIR="$PWD/.gotmp"
go test ./...

# 前端语法检查
node --check cmd/agentmemory/frontend/dist/app.js

# 开发模式 / 构建
cd cmd/agentmemory && wails dev
cd cmd/agentmemory && GOFLAGS=-trimpath wails build
```

要求：Go 1.26、Node 18+（仅插件与语法检查用）、Wails v2。分发构建固定带 `-trimpath`，否则本地路径会进 exe。

## 文档

- 分发与使用说明：[`docs/PORTABLE-README.md`](docs/PORTABLE-README.md)
- 需求与架构：[`docs/PRD.md`](docs/PRD.md)、[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
- 看板与进度：[`docs/ISSUES.md`](docs/ISSUES.md)

## 反馈

仓库已公开，问题与建议走 GitHub Issues。这个工具只在本机跑，不上传任何对话内容。
