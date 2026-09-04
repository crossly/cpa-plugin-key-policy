# cpa-key-policy（中文说明）

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的**下游 API Key 策略插件**。

用人话说：你可以给客户发自己的 `cpa_…` 钥匙。每把钥匙只能用你允许的模型，还能限速、限额，并转到 CPA 真实上游（Codex、Claude、openai-compatibility 通道等）。CPA 自带的 `api-keys` 仍可留给管理员；**不要把插件下发的 key 再写进 `api-keys`**，否则会绕过本插件策略。

| | |
|---|---|
| **仓库** | [origin652/cpa-plugin-key-policy](https://github.com/origin652/cpa-plugin-key-policy) |
| **协议** | MIT |
| **安装** | [CLIProxyAPI 插件商店](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store) 或自行编译 |
| **English** | [README.md](./README.md) |

---

## 它能干什么

1. **发钥匙** — 批量创建下游 key，每把绑定可用模型 / 别名。  
2. **做映射** — 客户端写 `model: fast`，插件转到例如 `codex` + `gpt-5.4-mini`。  
3. **做限制** — 单 key 的 RPM、可选每日/每周美元额度，按 token 或按次计费。  
4. **凭证分档 / 归类** — 请求可以钉死在 Codex free/team 等内置档，或你自定义的归类组，**不会串到别的凭证文件**。  
5. **多目标别名** — 一个别名挂多个后端（优先 或 轮询）。  
6. **网页管理** — 在 CPA 里管 key、全局别名、凭证归类。  

---

## 核心概念

### 下游 Key

插件自己发的密钥（`cpa_…`），只由本插件鉴权。上面可以配置：

- 允许的 **模型** 和/或 **别名**
- RPM
- 每日 / 每周美元上限（可选）
- 是否允许主端口访问 `/v1/models`（见下文）

### 别名（全局映射表）

可复用的名字，例如 `fast`，展开成一条或多条 **目标**：

| 字段 | 含义 |
|------|------|
| `provider` | CPA 提供商标识（`codex`、`claude`，或 openai-compatibility 的 **name**，如 `cerebras`） |
| `target_model` | 上游真实模型 id |
| `group` | 可选，限制用哪一类凭证（见下节） |
| `dispatch` | `priority`（始终尝试第一个）或 `round-robin`（轮询） |
| 计费 | `tokens`（百万 token 单价）或 `per_call`（每次固定金额） |

Key 可以**引用**别名，不必重复填目标。多目标别名会展开成多条同名规则；**同一次请求**里鉴权与路由共用同一次选择，保证 `group` 与真实目标一致。

### 凭证组：内置档位 + 自定义归类

| 类型 | 选择器里长什么样 | 写进映射的 group |
|------|------------------|------------------|
| **内置档**（Codex `plan_type`、Antigravity `tier`） | 如「免费档 / Team」 | 裸名：`free`、`team`、`supported` |
| **自定义归类** | 如 **「自定义 · vip」** | 带前缀：`classify:vip` |

**运行时规则：** 映射里写了 group，调度就**只**在该组凭证里选文件。没有可用文件 → 直接失败（`auth_not_found`），**绝不**偷偷落到其他档。

**调度：** 插件先保留可用凭证中 `Priority` 最高的一层，再按凭证的 `weight` 执行**平滑加权轮询**：

- 权重来自 CPA 凭证配置，插件兼容候选的 `Weight`、`Attributes.weight` 和 `Metadata.weight`。
- 未配置或无法解析时按 `1` 处理；非正数表示暂停接收新请求；最大按 `1000000` 处理。
- 轮询状态按 `provider + model + group + Priority` 全局共享，所有 `cpa_…` key 共用同一分配比例。group 为空时，CPA 为该 provider/model 提供的全部候选凭证形成一个全局池。
- 低 `Priority` 凭证仅在更高优先级凭证全部不可用或权重非正时参与。
- CPA 未转发前端鉴权 group 元数据时（包括 CPA `7.2.140`），插件自有密钥仍会执行全局加权轮询，不再回退到 CPA 的 `routing.strategy`；CPA 原生密钥不受影响。
- 设置 `global_weighted_round_robin: true` 后，即使 CPA 能转发 group，插件也会主动忽略它，并在当前 provider/model 的全部候选凭证中按 Weight 调度。

**自定义归类**（网页 → 映射 → 凭证归类）：

- 用正则匹配凭证字段（`filename`、`provider`、`plan_type`、`tier` 等）。
- 规则上保存你起的**组名**（裸名）。
- 目录与映射使用 `classify:组名`，避免和内置的 `free`/`team` 撞名。
- 一个文件可命中**多个**自定义组（选择器里每组都会出现）。
- 没命中自定义规则 → Codex/Antigravity 走内置档；其它 auth-file 渠道默认扁平（无组）。
- openai-compat / API Key 类通道保持**扁平**，不拆组。

一般在网页或管理 API 配置即可，不必手改 state JSON。

### openai-compatibility 通道

CPA 里配置的兼容通道，映射时 `provider` 填通道 **name**。插件路由时会对应到主机内部的 `openai-compatible-<name>`。通道配置里的 **models 列表要写全**，否则主机会报「该模型无可用 auth」。

---

## 插件能力一览

| 钩子 | 作用 |
|------|------|
| 前端鉴权 | 识别插件 key；校验别名、RPM、额度；写入路由与 group 元数据 |
| 模型路由 | 别名 → provider + 目标模型 |
| 调度 | 可选按 group 档位 / `classify:` 过滤，并在最高 Priority 层内平滑加权轮询 |
| 请求拦截 | schema 2：上游鉴权前返回 HTTP 429 策略错误；旧主程序保持 fail-closed 鉴权行为 |
| 响应拦截 | 非流式 JSON：把顶层 `model` 改回别名 |
| 用量 | token / 按次计费写入 state |
| 管理 API + 内嵌网页 | Key、别名、归类、状态 |

---

## 编译

Linux `.so` 需要 cgo：

```bash
make test
make build-linux          # 先编前端，再编 linux amd64/arm64 .so
# 或
make web-build
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build -buildvcs=false -tags cshared \
  -buildmode=c-shared -o dist/cpa-key-policy_linux_amd64.so ./cmd/cpa-key-policy
```

Windows 上请用 WSL/Linux 编 `.so`。`go test ./...` 可用非 cgo stub，不依赖动态库工具链。

把 `.so` 放进 CPA 的 `plugins.dir`，并在配置里启用插件。

---

## 配置

最小形态（完整示例见 [`config.example.yaml`](./config.example.yaml)）：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-policy:
      enabled: true
      priority: 10
      state_file: "cpa-key-policy-state.json"
      global_weighted_round_robin: true
```

说明：

- 若已有 `state_file`，则以其中的 keys / 别名 / 归类 / 用量为准。
- `global_weighted_round_robin: true` 会忽略别名目标选中的 group，把当前 provider/model 的所有候选凭证放入同一个全局权重池。默认为 `false`。
- 开启该选项后，别名层的 group 仍会轮询，但不再限制最终凭证；分配比例只由 CPA 凭证页的 Weight 决定。
- 日常请用**网页**或管理 API 建 key 和别名；YAML 种子数据主要用于首次启动。
- 公开文档里不要写真实管理密钥、主机名或凭证内容。

---

## 网页管理界面

插件内嵌。加载后访问：

```text
http://<你的-cpa-主机>:<api端口>/v0/resource/plugins/cpa-key-policy/index.html
```

用 CPA **管理密钥**登录（`remote-management.secret-key` 或管理密码）。密钥只放在内存，不写 `localStorage`；刷新页面需重新登录。

| 区域 | 用途 |
|------|------|
| Keys | 创建/编辑/轮换/删除 key；绑定模型或别名；RPM 与额度；手动重置每日/每周用量 |
| 映射 → 别名 | 全局多目标别名、调度方式、定价 |
| 映射 → 凭证归类 | 自定义分组规则与命中预览 |
| 选模型 | 提供商目录；内置档 / **自定义 · …** 子组 |

不重编 `.so` 时开发前端：

```bash
cd web
npm install
VITE_CPA_BASE=http://127.0.0.1:8317 npm run dev
```

---

## 管理 API 摘要

路径为精确匹配。鉴权：CPA 管理 Bearer。

**Key：**

- `GET/POST/PATCH/DELETE …/keys`（变更操作的 `id` 在 query 或 body）
- `POST …/keys/rotate?id=…`
- `POST …/keys/reset-rpm?id=…`
- `POST …/keys/reset-usage?id=…` — 清除该 key 的每日/每周用量与每别名统计，并立即持久化
- `GET …/keys/usage?id=…`
- `GET …/status`

`reset-usage` 只清除当前每日/每周 ledger 和每别名统计，不会修改 key、额度上限或 RPM 计数；`reset-rpm` 仍是独立操作。

**别名：** `GET/POST/DELETE …/aliases`  

**归类：**  

- `…/classify-rules`（含 reorder）  
- `POST …/classify-preview` — 预览组 → 凭证 id（组名为规则裸名）  
- `POST …/catalog` — 前端提交 auth-file + 模型列表，返回带 `classify:` 的选择器条目  

创建 key（`plain_key` **只返回一次**）：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/keys" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "team-a",
    "name": "Team A",
    "rpm": 60,
    "models": [
      {"alias":"fast","provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'
```

多目标别名示例：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-key-policy/aliases" \
  -H "Authorization: Bearer $MANAGEMENT_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "alias": "cheap-chat",
    "dispatch": "priority",
    "billing_mode": "tokens",
    "targets": [
      {"provider":"cerebras","target_model":"gpt-oss-120b"},
      {"provider":"codex","target_model":"gpt-5.4-mini","group":"free"}
    ]
  }'

## 客户端请求行为

| 情况 | 结果 |
|------|------|
| 认识的 key + 允许的别名 | 鉴权通过 → 路由 → 可选 group 过滤 → 上游 |
| 不允许的模型名 | 鉴权失败 |
| 超 RPM | HTTP 429，按请求原生协议返回；OpenAI 兼容路径为 `rate_limit_error` / `rate_limit_exceeded` |
| 超每日 / 每周美元额度 | HTTP 429，动态返回用量、上限、重置时间和 `Retry-After`；OpenAI 兼容路径使用 `insufficient_quota` JSON |
| 写了 group 但组内无可用凭证 | `auth_not_found` / 不可用（不串档） |
| 不认识的 key | 插件放弃，CPA 可尝试原生 `api-keys` |
| 非流式对话响应 | 顶层 `model` 改回别名 |
| 流式 | v1 不改写 body |

额度超限响应由请求拦截器在上游鉴权选择前生成；只有 CPA 主程序协商到插件协议 schema 2 或更高版本时才可用。

### 主端口的 `/v1/models`

每 key 的 `allow_models_endpoint` 是**开关**：拒绝（401）或看**全局完整列表**。主端口无法按插件 key 过滤列表。被拒绝但已知的 key 仍然是有效 key；当前 401 是 CPA 前端鉴权协议的限制，因为该端点不经过请求拦截链。要返回准确的 403，需要主程序支持明确的鉴权拒绝响应。


---

## 上手清单

1. 编译/安装 `.so` 到 CPA `plugins.dir`。  
2. 启用 `plugins` 与 `cpa-key-policy`，配置 `state_file`。  
3. 用管理密钥打开网页 UI。  
4. （可选）配置**凭证归类**规则。  
5. 建**别名**（多目标/定价）和/或给 key 勾选模型（含档位或「自定义 · …」）。  
6. 创建 key，保存一次性 `plain_key`，发给客户。  
7. 客户：OpenAI 兼容 base URL = CPA；`Bearer cpa_…`；`model` = 别名。  
8. openai-compat 通道务必声明 models，否则会「无 auth」。

---

## 测试

```bash
go test ./...
cd web && npm test && npm run build
```

