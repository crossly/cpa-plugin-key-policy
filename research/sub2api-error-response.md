# Sub2API 额度错误响应调查

## 调查范围

- 官方仓库：[`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api)
- 调查分支：`main`
- 调查源码快照：[`0d27f45ead1b58908548ec21afd923ecaf7339bc`](https://github.com/Wei-Shaw/sub2api/commit/0d27f45ead1b58908548ec21afd923ecaf7339bc)
- 最新已发布版本：[`v0.1.185`](https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.185)，其 tag 指向 `2ac784c51a5d0925b324efef2ba6b3446c364781`。发布页显示该版本之后 `main` 又有 8 个提交，因此本文以当前 `main` 源码为主，并注明发布版未发现与关键实现相关的差异。

## 结论一：API Key 总额度用完

### 触发逻辑

`backend/internal/server/middleware/api_key_auth.go` 中的 `abortWithAPIKeyQuotaError` 固定使用 HTTP `429 Too Many Requests`。当请求路径属于 Responses API 时，调用 `abortWithOpenAIQuotaError`；其它路径走旧的通用错误格式。

源码：[`api_key_auth.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/server/middleware/api_key_auth.go#L308-L327)

Responses 路径判断：[`isOpenAICompatibleAPIKeyRequest`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/server/middleware/api_key_auth.go#L323-L341)

当前判断覆盖：

- `/v1/responses`
- `/openai/v1/responses`
- `/responses`
- `/backend-api/codex/responses`
- 上述路径的子路径

### OpenAI 格式响应

`abortWithOpenAIQuotaError` 的实际实现为：

```json
{
  "error": {
    "message": "API key 额度已用完",
    "type": "insufficient_quota",
    "param": null,
    "code": "insufficient_quota"
  }
}
```

HTTP 状态为：

```text
429 Too Many Requests
```

源码：[`middleware.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/server/middleware/middleware.go#L83-L95)

### 官方测试证据

`TestAPIKeyAuthOpenAIQuotaErrorFormat` 明确断言：

- 状态码是 `http.StatusTooManyRequests`，即 `429`；
- `error.message` 是 `API key 额度已用完`；
- `error.type` 是 `insufficient_quota`；
- `error.param` 为 `null`；
- `error.code` 是 `insufficient_quota`。

测试：[`api_key_auth_test.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/server/middleware/api_key_auth_test.go#L1436-L1476)

## 结论二：非 Responses 路径保留旧格式

对 `/v1/messages` 等非 Responses 路径，测试断言仍是：

```json
{
  "code": "API_KEY_QUOTA_EXHAUSTED",
  "message": "API key 额度已用完"
}
```

状态仍为 `429`，但不是 OpenAI 的 `{ "error": { ... } }` 结构。

测试：[`api_key_auth_test.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/server/middleware/api_key_auth_test.go#L1478-L1505)

因此，Sub2API 不是对所有协议强制使用同一种错误体，而是按入口协议/路径选择响应格式：

| 场景 | HTTP | 响应格式 | 核心字段 |
|---|---:|---|---|
| API Key 总额度耗尽 + Responses | 429 | OpenAI | `type=insufficient_quota`, `code=insufficient_quota`, `param=null` |
| API Key 总额度耗尽 + 其它旧路径 | 429 | Sub2API 旧格式 | `code=API_KEY_QUOTA_EXHAUSTED` |

注意：源码中的 `isOpenAICompatibleAPIKeyRequest` 当前只识别 Responses 路径。根据这段路径判断可以推断，`/v1/chat/completions` 不会进入该专用分支；仓库现有额度格式测试直接覆盖的是 `/v1/responses` 与 `/v1/messages`，没有单独覆盖 `/v1/chat/completions`。

## 结论三：用户/平台时间窗口额度

Sub2API 还存在另一类额度：用户 × 平台的日/周/月额度。其内部错误分别是：

- `USER_PLATFORM_DAILY_QUOTA_EXHAUSTED`
- `USER_PLATFORM_WEEKLY_QUOTA_EXHAUSTED`
- `USER_PLATFORM_MONTHLY_QUOTA_EXHAUSTED`

这些错误本身构造为 HTTP `429`，并携带 `window_resets_at` 元数据：

源码：[`billing_cache_service.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/service/billing_cache_service.go#L35-L38)、[`billing_cache_service.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/service/billing_cache_service.go#L1071-L1077)、[`billing_cache_service.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/service/billing_cache_service.go#L1305-L1313)

`billingErrorDetails` 对这三类错误返回：

- HTTP `429`；
- 内部映射码 `rate_limit_exceeded`；
- 原错误消息；
- 根据 `window_resets_at` 计算的 `Retry-After` 秒数。

源码：[`gateway_handler.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/handler/gateway_handler.go#L2412-L2465)

相关测试明确要求额度时间窗口错误返回 `429` 和正的 `Retry-After`，并验证约 3600 秒的窗口值：

测试：[`gateway_handler_billing_error_test.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/handler/gateway_handler_billing_error_test.go#L75-L123)

### OpenAI 入口的时间窗口响应

OpenAI 网关通过 `handleStreamingAwareError` 输出错误。按当前调用关系，非流式响应的形状是：

```json
{
  "error": {
    "type": "rate_limit_exceeded",
    "message": "Daily usage quota exhausted for this platform."
  }
}
```

同时附带：

```text
Retry-After: <距离窗口重置的秒数>
```

这里的 `rate_limit_exceeded` 是 `error.type`，不是 API Key 总额度分支中的 `error.code`。当前这条平台额度路径没有统一补 `param` 或独立 `error.code`。这是根据 `billingErrorDetails` 的调用参数和 `handleStreamingAwareErrorWithCode` 的源码组合得出的源码结论；相关测试主要验证状态码、映射码和 `Retry-After`，没有直接断言最终 JSON body。

源码：[`openai_gateway_handler.go`](https://github.com/Wei-Shaw/sub2api/blob/0d27f45ead1b58908548ec21afd923ecaf7339bc/backend/internal/handler/openai_gateway_handler.go#L3328-L3368)

## 对本插件的借鉴

### 可以直接借鉴

1. **额度耗尽使用 HTTP 429**，而不是 401 或 403。
2. OpenAI 兼容入口使用标准错误对象：

   ```json
   {
     "error": {
       "message": "...",
       "type": "insufficient_quota",
       "param": null,
       "code": "insufficient_quota"
     }
   }
   ```

3. 对有明确重置时间的日/周额度，设置 `Retry-After`。
4. 不把额度耗尽误报为 `invalid_api_key`。

### 不应直接照搬

1. Sub2API 的 `API_KEY_QUOTA_EXHAUSTED` 是其数据库 Key 状态错误码；本插件当前的内部原因是 `daily_exceeded` / `weekly_exceeded`，二者属于不同层级。
2. Sub2API 能在自己的 HTTP middleware 中直接写响应；本插件的 `frontend_auth.authenticate` 只有布尔鉴权结果，CPA Host 当前会丢弃未认证响应中的插件自定义错误字段。
3. Sub2API 对 Responses 和旧协议采用不同响应体。本插件如只面向 OpenAI 兼容请求，可以采用 OpenAI 体；若还要覆盖 Claude/Gemini 入口，应按入口协议分别生成响应体。

## 对后续实施的约束

在不改 CPA 主程序的前提下，若要让客户端真正收到上述 JSON，插件必须利用 CPA 已有的“请求拦截终止”能力，在鉴权通过后的执行前阶段直接返回 `429` 和完整 body。仅从 `frontend_auth.authenticate` 返回自定义 RPC 错误不能达到目的，因为当前 CPA Host 会把该结果归类为 `NotHandled`，随后生成通用鉴权错误。

实施前需要确认运行中的 CPA 版本支持：

- `request_interceptor` 能力注册；
- `request.intercept_before`；
- `Terminate`、`StatusCode`、`ResponseHeaders`、`ResponseBody` 字段。

如果运行中的 CPA 不支持这些能力，插件应继续保留当前额度拒绝逻辑，不能为了输出 429 而无条件放行额度耗尽的 Key，否则会形成额度绕过。
