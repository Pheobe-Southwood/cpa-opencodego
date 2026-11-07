# OpenCode Go 额度（只读）· cpa-opencodego-quota

一个 CLIProxyAPI v8 插件，用来查看宿主里每个 **OpenCode Go** API Key 的额度：
5 小时、本周、本月三个窗口的已用百分比、剩余百分比和重置时间。

只读。它不注册 provider / executor / 模型 / 路由，不写任何文件，也不参与请求转发。

## 背景

OpenCode Go 的 key 曾经由 `cpa-opencodego` provider 插件托管，插件 dashboard 里能看到
每个 key 的额度。迁移到 CPA 原生 provider 之后，这些 key 分散在宿主 config.yaml 的五个
原生分组里（gemini、claude、codex、openai-compatibility ×2），插件被移除，额度也就看不
到了。

CPA 管理面板的「额度」页只对内建 provider（claude、codex、antigravity 等）和声明了
`QuotaProvider` 的插件生效。OpenCode Go 的 key 现在挂在原生分组上，其中 claude / codex
分组会被面板的内建额度卡片接管——那些卡片调用的是 Anthropic / OpenAI 的额度接口，对
OpenCode Go 的 key 没有意义。因此这里选择注册一个**只读管理页**：一张页面统一列出宿主
配置里所有 OpenCode Go key 的额度，不依赖 provider 名，也不改动 CPA 源码。

## 它做什么

- 读取宿主 `config.yaml` 中的 `api-keys` 分组，筛出 `base-url` 指向 OpenCode Go 的组；
- 按 key 去重（同一个 key 常同时出现在直连组和代理组），逐个请求
  `…/inference/go/v1/usage`；
- 在管理面板侧边栏渲染「OpenCode Go 额度」页面：汇总、Key 卡片、路由分组表；
- 暴露同源的只读 JSON：`/v0/resource/plugins/cpa-opencodego-quota/api/quota`。

## 它不做什么

- 不注册 executor、模型目录、路由、usage 回调；
- 不写任何文件，不改宿主配置，不调用需要管理密钥的接口；
- 不显示完整 key：页面与 JSON 里都是掩码（`oc_sk_cd1a…q2N4`）；
- 不缓存到磁盘。

## 工作原理

1. **定位宿主配置**：插件与宿主同进程，`os.Args` 就是宿主参数，因此可以直接从
   `-config /CLIProxyAPI/conf/config.yaml` 拿到路径。依次尝试：
   `config-path` 插件配置 → 宿主 argv → `CPA_CONFIG_PATH` / `CLIPROXY_CONFIG_PATH` /
   `CLIPROXYAPI_CONFIG` → `/CLIProxyAPI/conf/config.yaml` →
   `/opt/cliproxyapi/conf/config.yaml` → `/root/cliproxyapi/conf/config.yaml` → `./config.yaml`。
   文件按 `mtime + size` 缓存，改完 config.yaml 刷新页面即可看到变化，无需重启。
2. **只读投影**：解析 `api-keys.gemini / claude / codex / openai-compatibility`（以及旧式
   顶层 `gemini-api-key` / `claude-api-key` / `codex-api-key` / `openai-compatibility`），
   只保留 base-url 主机名命中 `hosts`（默认 `opencode.ai`）的分组；`disabled: true` 的分组
   只出现在分组表里，不参与 Key 列表。
3. **额度探测**：每个唯一 key 一次 `GET …/inference/go/v1/usage`，`Authorization: Bearer`。
   出口语义与宿主一致：分组/Key 的 `proxy-url: direct` → 直连；否则继承宿主
   `requests.proxy-url`。也可以用插件配置强制 `egress: direct|proxy`。
4. **页面**：管理面板把插件页放进 iframe，页面会自动跟随面板的 `data-theme` 深/浅色，
   并测量面板 header 高度做顶部内缩；60 秒自动刷新一次，可关闭。

用量接口地址由 base-url 归一化得到：`…/inference/go/v1`、`…/inference/go/google`、
`…/inference/go/openai/v1`、`…/inference/go/anthropic` 都映射到
`…/inference/go/v1/usage`（OpenCode Go 的同一个用量端点）。

## 安装

```bash
# 构建 .so（需要 CGO 与 gcc）
make build

# 复制到 CPA 插件目录（默认 /opt/cliproxyapi，可用 CPA_ROOT 覆盖）
make install
```

然后在宿主 `conf/config.yaml` 的 `plugins.configs` 下加入：

```yaml
plugins:
    configs:
        cpa-opencodego-quota:
            enabled: true
```

重启 CLIProxyAPI，管理面板侧边栏会出现「OpenCode Go 额度」。

命令行自检（资源路由不需要管理密钥）：

```bash
curl -s http://127.0.0.1:8317/v0/resource/plugins/cpa-opencodego-quota/api/quota | jq .
curl -s http://127.0.0.1:8317/v0/resource/plugins/cpa-opencodego-quota/dashboard | head
```

## 配置字段

全部可选，写在 `plugins.configs.cpa-opencodego-quota` 下：

| 字段 | 默认值 | 说明 |
| --- | --- | --- |
| `config-path` | 自动发现 | 宿主 `config.yaml` 路径；留空则按上面的顺序自动发现 |
| `hosts` | `[opencode.ai]` | 视为 OpenCode Go 的 base-url 主机名列表（也匹配子域名） |
| `egress` | `auto` | 探测出口：`auto` 跟随分组的直连/代理；`direct` 强制直连；`proxy` 强制走 `proxy-url` |
| `proxy-url` | 空 | `egress: proxy` 时使用的代理；留空则用宿主 `requests.proxy-url` |
| `usage-url` | 自动推导 | 覆盖用量端点（上游改地址时使用） |
| `session` | 空 | 探测时附带的 `x-opencode-session` 头（当前端点不需要） |
| `timeout-seconds` | `20` | 单个 `/usage` 探测超时 |
| `cache-seconds` | `10` | 额度快照缓存时间；页面上的「刷新额度」会绕过缓存 |

## JSON 结构

`/api/quota` 返回：

```json
{
  "generated_at": "2026-11-07T17:10:00Z",
  "config_path": "/CLIProxyAPI/conf/config.yaml",
  "global_proxy": "http://127.0.0.1:7890",
  "usage_url": "https://opencode.ai/inference/go/v1/usage",
  "summary": {"keys": 6, "ok": 6, "failed": 0, "groups": 5, "max_rolling_percent": 2},
  "groups": [
    {"key": "openai-compatibility:opencode-chat-direct", "family": "openai-compatibility",
     "name": "opencode-chat-direct", "base_url": "https://opencode.ai/inference/go/v1",
     "priority": 10, "egress": "direct", "key_count": 6,
     "models": [{"name": "deepseek-v4-pro", "alias": "DeepSeek-V4-Pro"}]}
  ],
  "keys": [
    {"id": "9a1f…", "masked": "oc_sk_cd1a…q2N4", "egress": "direct",
     "groups": ["gemini:opencode-gemini-direct", "openai-compatibility:opencode-chat-direct"],
     "windows": [
       {"key": "rolling", "label": "5 小时", "status": "ok", "percent": 2,
        "remaining_percent": 98, "resets_at": "2026-11-07T19:11:22Z"}
     ],
     "fetched_at": "2026-11-07T17:10:00Z"}
  ]
}
```

宿主配置解析失败时仍返回 200，`config_error` 字段说明原因，页面顶部显示红色横幅；
单个 key 探测失败只影响该 key 的卡片。

## 限制

- 需要能读到宿主的 `config.yaml`。如果容器把配置放在非常规路径，设置 `config-path` 即可。
- 额度是 **key 的属性**，与分组无关；同一个 key 被多个分组引用时只显示一张卡片，并列出它
  服务的分组。
- `disabled: true` 的 OpenCode Go 分组不出现在 Key 列表里（只在分组表里标注「已停用」），
  因为宿主不会用它们路由。
- CPA 管理面板原生的「额度」页对 claude / codex 内建 provider 有专用卡片，会接管这些
  provider 下的 Key；本插件的页面是唯一能统一看到全部 OpenCode Go Key 的地方。

## 安全

- 只读：插件不写配置、不写文件、不注册任何能影响路由的能力；唯一的 host 回调是初始化时
  的 ABI 绑定。
- Key 在页面和 JSON 里都是掩码；完整 key 只在插件进程内存中用于一次
  `Authorization: Bearer` 请求。
- 页面设置了 `Content-Security-Policy`（`default-src 'none'`，只允许同源 fetch）。
- 与其他插件的资源页一样，`/v0/resource/plugins/...` 路由不受管理密钥保护。本部署里
  CLIProxyAPI 只监听 `127.0.0.1:8317`，因此页面仅本机可达；不要把 8317 暴露到公网。

## 归档

旧的 OpenCode Go provider 插件（chat / responses / messages / gemini 四个协议面、v2.0.14）
已归档，main 分支从本插件重新开始：

- 分支：`archive/opencode-go-plugin-v2.0.14`
- 标签：`archive-v2.0.14`

## 开发

```bash
make test    # go test ./...
make vet
make build   # dist/cpa-opencodego-quota-v1.0.0.so
```

目录：

- `cmd/plugin`：CLIProxyAPI 的 C ABI 入口（c-shared）；
- `internal/plugin`：注册、管理路由、宿主配置只读投影、`/usage` 探测；
- `internal/dashboard`：页面 HTML / CSS / JS；
- `internal/abi`：宿主 RPC 的 JSON envelope。

## 许可

MIT。OpenCode 图标 © 2025 opencode（https://opencode.ai，MIT），仅用于标识本插件读取的
上游服务。
