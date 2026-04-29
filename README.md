# kie2api-go

把 [KIE.AI](https://kie.ai) 的 Chat 模型一键暴露成 **OpenAI 兼容** 与 **Anthropic 兼容** 两套接口，
方便 Codex / OpenCode / Claude Code / Hermes / Openclaw 等客户端直接接入。

- ✅ 写一次 KIE.AI API Key，所有客户端共用
- ✅ 自动协议互转（OpenAI Chat ⇄ OpenAI Responses ⇄ Anthropic Messages）
- ✅ 支持流式（SSE）：同协议直通，跨协议事件级翻译
- ✅ Web 控制台勾选启用模型，未启用的模型不会暴露
- ✅ KIE 上游业务错误（HTTP 200 + `{"code":500,...}`）自动识别并向客户端返回标准错误
- ✅ 单二进制，零运行时依赖（仅用 Go 标准库），Docker 一行起服务

> 已用真实 KIE.AI Key 实测：OpenAI Chat ↔ Anthropic Messages 双向流式 / 非流式调用，跨协议
> 模型选择（如用 OpenAI 协议直接调 `claude-haiku-4-5`，或用 Anthropic 协议调 `gpt-5-2`）均工作正常。

---

## 快速开始

### 方式 1：本地源码运行

需要 Go 1.22+：

```bash
git clone https://github.com/Derrity/kie2api-go.git
cd kie2api-go
go build -o kie2api .
./kie2api
# Web 控制台:  http://localhost:3001
# 代理 API:    http://localhost:4142
```

打开 http://localhost:3001 ：

1. 填入 KIE.AI 的 API Key 并保存（控制台支持「连通性测试」一键验证）
2. 勾选要启用的模型，保存
3. 复制下方的 **Proxy API Key**（`sk-…48 hex`），用它去配置客户端

### 方式 2：Docker

```bash
docker build -t kie2api .
docker run -d --name kie2api \
  -p 3001:3001 -p 4142:4142 \
  -v $HOME/.kie2api:/data \
  --restart unless-stopped \
  kie2api
```

### 方式 3：docker-compose

仓库自带 `docker-compose.yml`：

```bash
docker compose up -d
```

数据（API Key、启用模型、Proxy Key）持久化在 `/data/config.json`。

---

## 支持的模型

通过 [docs.kie.ai/market/chat](https://docs.kie.ai/market/chat) 暴露的全部 Chat 模型：

| 分组       | 模型 ID                                                                                                      | 上游协议             |
|------------|--------------------------------------------------------------------------------------------------------------|----------------------|
| GPT        | `gpt-5-2`                                                                                                    | OpenAI Chat          |
| GPT        | `gpt-5-4`, `gpt-5-5`                                                                                         | OpenAI Responses     |
| GPT Codex  | `gpt-5-codex`, `gpt-5.1-codex`, `gpt-5.2-codex`, `gpt-5.3-codex`, `gpt-5.4-codex`                            | OpenAI Responses     |
| Claude     | `claude-haiku-4-5`, `claude-opus-4-5`, `claude-opus-4-6`, `claude-sonnet-4-5`, `claude-sonnet-4-6`           | Anthropic Messages   |
| Gemini     | `gemini-2-5-pro`, `gemini-3-pro`, `gemini-3-1-pro`, `gemini-2-5-flash`, `gemini-3-flash`                     | OpenAI Chat          |

**无论上游是什么协议，客户端都可以用任意一种协议调用** —— kie2api 会自动翻译。

---

## 客户端接入

### OpenAI 兼容（Codex / OpenCode / Hermes-OpenAI / 任意 OpenAI SDK）

```bash
export OPENAI_BASE_URL=http://localhost:4142/v1
export OPENAI_API_KEY=sk-你的proxy-key
```

```bash
curl http://localhost:4142/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role":"user","content":"你好"}],
    "stream": true
  }'
```

注意：你**可以用 OpenAI 协议直接调 Claude / Gemini 模型**，kie2api 会自动翻译请求和（流式）响应。

### Anthropic 兼容（Claude Code / Hermes-Anthropic / Openclaw）

```bash
export ANTHROPIC_BASE_URL=http://localhost:4142
export ANTHROPIC_AUTH_TOKEN=sk-你的proxy-key
export ANTHROPIC_MODEL=claude-sonnet-4-6
```

```bash
curl http://localhost:4142/v1/messages \
  -H "x-api-key: sk-你的proxy-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5-2",
    "max_tokens": 1024,
    "messages": [{"role":"user","content":"hi"}]
  }'
```

### OpenAI Responses 协议

```bash
curl http://localhost:4142/v1/responses \
  -H "Authorization: Bearer sk-你的proxy-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "gpt-5-codex",
    "input": [{"role":"user","content":"hello"}]
  }'
```

---

## 端点一览

| 路径                              | 协议                       | 说明                             |
|-----------------------------------|----------------------------|----------------------------------|
| `POST /v1/chat/completions`       | OpenAI Chat Completions    | 也支持 `/chat/completions`       |
| `POST /v1/responses`              | OpenAI Responses           | 也支持 `/responses`              |
| `POST /v1/messages`               | Anthropic Messages         |                                  |
| `POST /v1/messages/count_tokens`  | Anthropic                  | 简单字符长度估算（chars/4）      |
| `GET  /v1/models`                 | OpenAI                     | 列出已启用的模型                 |
| `GET  /health`                    | -                          | 探活                             |

所有 `/v1/*` 路径都需要 `Authorization: Bearer <proxy_key>` 或 `x-api-key: <proxy_key>`。

---

## 配置

| Flag           | 默认值                                              | 说明              |
|----------------|-----------------------------------------------------|-------------------|
| `--web-port`   | `3001`                                              | Web 控制台端口     |
| `--proxy-port` | `4142`                                              | 代理 API 端口      |
| `--data-dir`   | `~/.local/share/kie2api` 或 `$KIE2API_DATA_DIR`     | 配置文件目录       |
| `--verbose`    | -                                                   | 启动时多打一行日志 |

环境变量：

| 变量                | 说明                                                          |
|---------------------|---------------------------------------------------------------|
| `KIE2API_DATA_DIR`  | 配置文件目录（等同于 `--data-dir`）                            |
| `KIE2API_DEBUG`     | 非空时打印每次上游请求 / 响应体（前 2KB），用于排查 KIE 上游问题 |

控制台还支持自定义 **上游 Base URL** 和 **HTTP 代理**（高级选项里），适合自建反代或走出口代理。

---

## 协议翻译矩阵

| 客户端协议 ↓ \ 上游协议 → | OpenAI Chat | OpenAI Responses | Anthropic Messages |
|---------------------------|:-----------:|:----------------:|:------------------:|
| OpenAI Chat               | 直通        | ✅ 翻译          | ✅ 翻译            |
| OpenAI Responses          | ✅ 翻译     | 直通             | ✅ 翻译            |
| Anthropic Messages        | ✅ 翻译     | ✅ 翻译          | 直通               |

- **直通**：原样转发请求体 + SSE 字节级直通，性能最好，错误最少。
- **翻译**：请求体结构转换 + 响应体（含流式）事件级转换，覆盖 text / tool_use / usage / stop_reason。
  跨协议下复杂的 tool_use 流式增量是「best-effort」。

---

## 安全提示

- ⚠️ **请勿**把 web 控制台（默认 3001）暴露到公网 —— 它没有登录鉴权。
- ✅ 代理 API（默认 4142）有 proxy_key 鉴权，可暴露给受信任客户端。
- 推荐部署：放在 Caddy / nginx 反向代理后，或只通过 `localhost` / 内网访问。
- 如果 proxy_key 泄漏，进控制台点「重新生成」即可作废。

---

## 项目结构

```
.
├── main.go                  # 入口（双 server: web + proxy）
├── config/                  # 配置 + 持久化（~/.local/share/kie2api/config.json）
├── routing/                 # KIE.AI 模型 → 上游路径 / 协议 路由表
├── upstream/                # 共享 *http.Client（连接池 + 可选 HTTP 代理）
├── sse/                     # SSE Reader / Writer / Passthrough
├── translate/               # OpenAI Chat ⇄ Responses ⇄ Anthropic 互转
├── handler/                 # /v1/* 分派 + /api/* 控制台
├── web/                     # 控制台（embed.FS + Tailwind CDN 单页）
├── Dockerfile               # distroless 多阶段构建
└── docker-compose.yml
```

---

## License

[MIT](LICENSE)
