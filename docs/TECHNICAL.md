# CLIProxyAPI OpenCode Go 插件 — 技术路线与实现文档

本文档收录 `cpa-opencodego` 插件的架构原理、协议转换机制、技术构建流程、全量配置参数参考与已知技术细节。日常使用与功能特性请参阅 [README.md](../README.md)。

---

## 1. 架构：为什么是 4 个 .so

CLIProxyAPI (CPA) 的插件执行器（Executor）基于**声明式协议格式**工作：插件在注册时声明自身只接受并处理某一种线上格式（`executor_input_formats` / `executor_output_formats`），由 CPA 宿主负责将客户端传入的各种协议请求翻译为此格式，并将插件回传的数据翻译回客户端所需格式。

OpenCode Go 的 37 个模型分布在 4 个完全不同的上游端点上：

| 上游端点（新分族布局） | 旧单根布局（v1，继续兼容） | 协议格式 |
|---|---|---|
| `POST {go}/openai/v1/chat/completions` | `POST {base}/chat/completions` | `openai` |
| `POST {go}/openai/v1/responses` | `POST {base}/responses` | `codex` + `openai-response` |
| `POST {go}/anthropic/v1/messages` | `POST {base}/messages` | `claude` |
| `POST {go}/google/v1beta/models/{id}:generateContent` | `POST {base}/models/{id}:generateContent` | `gemini` |
| `GET {go}/v1/models`、`GET {go}/v1/usage` | `GET {base}/models`、`GET {base}/usage` | 列表 / 额度 |

`{go}` 默认是 `https://opencode.ai/inference/go`；`{base}` 是旧值如 `https://opencode.ai/inference/go/v1` 或 `https://opencode.ai/zen/go/v1`。布局由 `endpoint-layout`（默认 `auto`，按 base-url 是否以 `/v1` 结尾判断）决定，可用 `base-urls` 逐族覆盖。

由于格式声明是 Provider 级别的，而具体模型所需的协议是模型级别的，单个 Provider 实例无法在不手写全套转换器的情况下承载多种上游协议。

因此，同一套 Go 代码通过不同的链接期编译常量（`-ldflags -X main.role=...`）编译出 4 个动态链接库（`.so`），分别对应 4 个协议面：

| 插件 ID | Provider Key | 声明格式 | 对应上游端点 |
|---|---|---|---|
| `cpa-opencodego` | `opencode-go` | `["openai"]` | `/chat/completions` |
| `cpa-opencodego-responses` | `opencode-go-responses` | `["codex","openai-response"]` | `/responses` |
| `cpa-opencodego-messages` | `opencode-go-messages` | `["claude"]` | `/messages` |
| `cpa-opencodego-gemini` | `opencode-go-gemini` | `["gemini"]` | `models/{id}:generateContent` |

### 统一控制与配置流

- **主实例（`cpa-opencodego`）**：唯一挂载 `management_api` 与侧边栏菜单的面，也是唯一的配置入口。它把解析后的配置写入 `<CPA 根>/data/cpa-opencodego/shared.json`。
- **从属实例**：其余 3 个从属面以只读模式监听 `shared.json` 的 mtime 变化进行惰性加载。从属面在宿主配置中仅需 `enabled: true`，不暴露重复冗余的配置字段。
- **模型路由抢占**：4 个面均注册各自的 `model_router`，按模型名单精准认领。由于设置了专属路由，可以稳定接管同名模型请求。

数据流向：

```
客户端(任意协议) → CPA 宿主(翻译为声明格式) → 插件(路由/选 Key/选网络通道) → 原样 POST 上游
                 → 上游流式返回 → CPA 宿主(翻译回客户端协议) → 客户端
```

插件自身不改写 JSON 协议体业务内容（仅对齐 `stream` 传输模式）。

---

## 2. 统计监控与延迟口径实现

主面 `cpa-opencodego` 声明了 `usage_plugin` 能力，接收宿主在请求完成后投递的 Usage 事件。Token 消耗、缓存命中有无、失败状态与总延迟均由宿主统一解析。

由于宿主对异步流桥接不提供内部 TTFT（`ttft_ns=0`），4 个协议面各自在读取上游 SSE 数据流时自行测量首个内容帧的时间戳，写入 `<状态目录>/stats-<role>.json`。主面的 `/api/stats` 接口自动汇聚 4 个协议面的耗时与用量。

### 指标口径

- **整体 TPS**：成功请求的有效 output tokens ÷ 宿主请求总耗时（包含首字、排队与全链路网络延迟）。
- **生成 TPS**：插件实测有效 output tokens ÷（最后一个内容帧时间 - 首个内容帧时间），不包含 TTFT，贴近纯解码速率。
- **TTFT**：向上游发起请求到接收到首个真实有效内容帧的时间差。空流、错误帧或提前中断不计入样本。
- **刊例价与额度**：内置官方价目快照折算，支持根据 `plan`（`go` / `go-plus`）自动计算各模型当月额度消耗百分比。

### 请求日志（v2.0）

`/api/requests` 合并四个协议面各自的 `requests-<role>.jsonl`，返回逐条记录：时间、模型、协议面、Key、输入/输出/合计 token（含缓存与推理明细）、TTFT、生成 TPS、端到端 TPS、状态、错误摘要、上游 `x-opencode-log-id` 与 8 位会话哈希。

- **数据来源**：executor 在转发时自行埋点并解析上游响应帧的 usage（chat 的 `usage`、messages 的 `message_start`/`message_delta`、responses 的 `response.completed`、gemini 的 `usageMetadata`），不依赖宿主 usage 事件，也不需要请求 id 关联。
- **口径**：生成 TPS = 有效输出 ÷（首内容帧 → 末内容帧）；端到端 TPS = 有效输出 ÷ 总耗时；TTFT 只统计有内容且正常结束的流；失败请求不计算 TPS。
- **保留**：每面最多 2000 条 / 7 天，文件超过 4 MiB 自动压缩；可在设置里关闭。
- **隐私**：不记录提示词、响应正文、Key 密文或原始会话 ID。

### 思考模式与角色兼容（v2.0.2 默认 retry-only 懒修复）

OpenCode Go 新网关的 gRPC 前端对部分节点存在两处偶发的 400 严格校验：
1. **developer 角色**：`chat message role developer is not supported by the router gRPC frontend`。
2. **思考模式 reasoning_content 回传**：`The reasoning_content in the thinking mode must be passed back to the API.`。

在 v2.0.2 中，这两处修复统一采用**按需懒修复（retry-only）+ 会话记忆（session sticky）**策略：

1. **默认 `developer-role: retry-only`**：首次请求逐字节保留原始 `developer` 角色透传；仅当命中上述 developer 400 时才改写为 `system` 并立即重试一次。`rewrite` 模式保持主动预先改写，`keep` 模式保持纯透传不重试。
2. **默认 `reasoning-replay: retry-only`**：首次请求保留原始历史透传；仅当命中上述 reasoning 400 时，才为历史 assistant 消息补齐中性占位符 `[reasoning unavailable]` 并重试一次。**重试成功后自动为该 `x-opencode-session` 开启 sticky 记忆**（TTL 2 小时，上限 4096 条），后续该会话请求提前补齐，既避免每一轮都承担 400 重试往返，又稳定了前缀缓存（Prompt Cache）命中率。
3. **修复阶梯保护**：单次请求最多允许 3 次上游尝试（developer 修复与 reasoning 修复各一次），同一修复不重复尝试，非 400 或不可恢复错误立即返回。
4. **流截断诊断**：请求日志新增 `repairs`、`finish`（`stop`、`length`、`tool_calls`、`eof`、`error`）与 `incomplete` 字段；当上游 SSE 未返回 `finish_reason` 即异常断开时，请求日志状态列清晰标出「截断」，便于定位网络与网关中途丢流问题。

改写只重编码 `messages` 数组中受影响的对象，其余顶层字段保持逐字节不变。

---

## 3. 环境要求与构建

### 环境要求

- **运行宿主**：CLIProxyAPI v8.x，且编译时开启 `CGO_ENABLED=1`（具备 `X-CPA-SUPPORT-PLUGIN: 1` 标识）。
- **构建环境**：Go 1.26+（经 go1.27.2 实测通过）。
- **插件 ABI**：采用标准 `dlopen` + C ABI（`abi_version = 1`），不要求与 CPA 宿主使用完全相同的编译器工具链或依赖版本。

### 编译命令

```bash
export PATH=/opt/go/bin:$PATH
export GOPROXY=https://goproxy.cn,direct GOSUMDB=off

# 编译 4 个协议面 .so
make build VERSION=2.0.2

# 执行单元测试与代码检查
make test
make vet
```

编译产出位于 `dist/` 目录：
- `dist/cpa-opencodego-v2.0.2.so`
- `dist/cpa-opencodego-responses-v2.0.2.so`
- `dist/cpa-opencodego-messages-v2.0.2.so`
- `dist/cpa-opencodego-gemini-v2.0.2.so`

---

## 4. 全量配置参数参考

所有核心配置集中在宿主 `config.yaml` 的 `plugins.configs.cpa-opencodego` 节点下：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-opencodego:
      enabled: true
      base-url: "https://opencode.ai/inference/go"
      endpoint-layout: "auto"
      developer-role: "retry-only"
      reasoning-replay: "retry-only"
      request-log: true
      session-mode: "auto"
      session: "$CPA-SESSION-ID"
      default-proxy: "inherit"
      proxy-url: ""
      request-timeout-seconds: 900
      plan: "go"
      debug: false
      keys:
        - name: "primary-key"
          key: "oc_sk_xxx"
          priority: 10
          enabled: true
        - name: "backup-key"
          key: "oc_sk_yyy"
          priority: 5
          enabled: true
      models:
        "deepseek-v4.1-flash":
          enabled: true
          proxy: "proxy"
          aliases: ["DeepSeek-V4.1-Flash"]
        "gemini-4-argon":
          enabled: true
          proxy: "direct"
    cpa-opencodego-responses: { enabled: true }
    cpa-opencodego-messages:  { enabled: true }
    cpa-opencodego-gemini:    { enabled: true }
```

### 字段说明表

| 字段 | 类型 | 默认值 | 作用与说明 |
|---|---|---|---|
| `base-url` | string | `https://opencode.ai/inference/go` | Go 根（新分族布局）；旧值 `.../inference/go/v1`、`.../zen/go/v1` 仍兼容并按 legacy 布局请求 |
| `endpoint-layout` | enum | `auto` | `auto` 按 base-url 判断、`legacy` 旧单根、`family` 新分族 |
| `base-urls` | map | `{}` | 逐族 API 根覆盖：`{chat, responses, messages, gemini}`，设置的族不再追加前缀 |
| `developer-role` | enum | `retry-only` | OpenAI 形状请求的 `developer` 角色处理：`retry-only` 默认原样透传并在命中 400 时改写为 `system` 重试一次，`rewrite` 预先改写，`keep` 关闭 |
| `reasoning-replay` | enum | `retry-only` | 思考模式历史 `reasoning_content` 回放：`retry-only` 默认命中 400 时补齐并重试，并开启会话 sticky 记忆；`auto` 只在思考请求提前补齐；`always` 所有 chat 请求补齐；`off` 不改写 |
| `request-log` | bool | `true` | 是否写逐条请求日志（每面 2000 条 / 7 天） |
| `keys` | array | `[]` | Key 列表，包含 `name`、`key`、`priority`、`enabled`。插件把每个 Key 投影成 CPA 认证文件（每个协议面 1 个），由 CPA AuthManager 选择、轮询、冷却并故障转移 |
| `models` | map | `{}` | 逐模型个性化配置（启用状态、代理策略、别名、协议面覆盖） |
| `models-default` | object | `{}` | 未显式声明模型的默认基线行为 |
| `default-proxy` | enum | `inherit` | 全局默认代理模式：`inherit` / `proxy` / `direct` |
| `proxy-url` | string | `""` | 插件专用出站代理地址；留空时自动继承宿主 `requests.proxy-url` |
| `session-mode` | enum | `auto` | 会话传递模式：`auto` 继承 CPA 会话 ID；`fixed` 使用固定值 |
| `session` | string | `"$CPA-SESSION-ID"` | 当 `session-mode=fixed` 时作为 `x-opencode-session` 报头值 |
| `request-timeout-seconds` | int | `900` | 上游单次请求最大超时时长（秒） |
| `plan` | enum | `go` | 订阅套餐类型：`go`（$10/月）或 `go-plus`（$40/月），决定月额度参考线 |
| `state-dir` | string | `<root>/data/cpa-opencodego` | 插件本地数据目录，存放 `shared.json`、各协议面统计文件与 `requests-<role>.jsonl`；首次启动会把 v1 目录的统计复制过来 |
| `debug` | bool | `false` | 是否开启调试模式，开启后输出详细追踪至 `<state-dir>/debug.log` |

### 代理生效链

请求代理按照如下优先级严格判定：
1. **模型级代理 (`models.<id>.proxy`)**：显式设定为 `direct`（直连）或 `proxy`（代理）。
2. **默认代理 (`default-proxy`)**：当模型设置为 `inherit` 时生效。
3. **插件级代理 (`proxy-url`)**：当上述判定需要走代理时优先使用的代理地址。
4. **宿主全局代理 (`requests.proxy-url`)**：当插件未设置专用代理时继承宿主配置。

### Key 调度与认证文件

v2.0.3 起，Key 的选择、优先级分层、同层轮询、429/401/403 冷却和失败转移全部由 CPA 宿主的
`AuthManager` 完成；插件只负责把配置投影成宿主认证文件：

- 每个 Key 生成 4 个文件：`<auth-dir>/opencode-go-<slug>.json`、`--responses.json`、
  `--messages.json`、`--gemini.json`，分别对应四个 provider；
- 文件字段：`type`、`name`、`key_name`、`role`、`api_key`、`priority`、`disabled`、`note`、
  `managed_by: cpa-opencodego`；权限 0600，原子写入，内容未变不重写；
- 投影单向：插件配置是源；在 CPA 认证文件页改启停/优先级会被下一次同步覆盖；
- 卸载/停服时 `Shutdown` 删除标记文件；`scripts/uninstall.sh` 按 `cpa-opencodego` 与旧版
  `cap-opencode-go` 两个标记兜底清理。

宿主必须满足以下配置（hk-vps 已满足）：

```yaml
routing:
  strategy: "round-robin"       # 或 fill-first
  retry:
    request-retry: 3
    max-retry-credentials: 0    # 0 = 同一轮尝试所有凭据；设为 1 会禁用同请求换 Key
    max-retry-interval: 30
  cooldown:
    disable-cooling: false      # true 会禁用 429/401 冷却
    save-cooldown-status: false # 插件同步不覆盖持久化冷却文件
```

无 `Retry-After` 的 429 冷却从 1s 开始倍增，上限 30m；401/402/403 为 30m；404 为 12h。
一次客户端请求在失败转移时会在插件请求日志里留下两条“上游尝试”（失败 + 成功），客户端只看到
最终状态。多 Key 且宿主没有传认证时，插件返回 503 `auth_not_selected`，不会使用第一把 Key；
单 Key 部署保留唯一 Key 的兜底。

---

## 5. 卸载与零残留设计

插件严格遵守零入侵、零残留原则：
- 不修改 CPA 源码与二进制；
- 不向宿主 SQLite/bbolt 写入任何脏数据；
- 不污染 CPA 宿主系统配置。

### 卸载步骤

1. 停止 CPA 服务或在运行中卸载：
   ```bash
   bash scripts/uninstall.sh
   ```
2. 从 `config.yaml` 的 `plugins.configs` 列表中移除 `cpa-opencodego` 及其三个从属协议面配置块。
3. 重启 CPA 宿主即可恢复纯净状态；插件 `Shutdown` 会删除 auth 目录里的标记文件，
   `scripts/uninstall.sh` 也会按标记兜底清理（含 v1.8–v1.11 的旧标记）。

---

## 6. 历史数据回放工具

针对先前使用 `cap-token-usage-tracker` 的环境，仓库附带 `cmd/replay-tracker` 工具，支持无损读取旧 bbolt 数据库，按最新价目表重算并导入历史调用记录：

```bash
# 停止 CPA 避免写冲突
systemctl stop cliproxyapi

# 编译并执行回放
make VERSION=2.0.1 replay
./dist/replay-tracker-v2.0.1 \
  -tracker /root/cliproxyapi/data/token-usage-tracker.db \
  -stats-dir /root/cliproxyapi/data/cpa-opencodego -apply

# 恢复服务
systemctl start cliproxyapi
```
