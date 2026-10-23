# 海龟汤 Go 服务端

固定房间 `1/2/3` 的海龟汤 MVP。服务端维护房间、发言权、局时和单人发言计时，复用设备现有 `hello`、`listen`、`tts` 控制消息及 Opus 音频帧格式。

## 启动

```bash
cd server
GAME_TOKEN=dev-secret GAME_ADDR=:8080 GAME_DB=:memory: go run ./cmd/server
curl http://127.0.0.1:8080/healthz
```

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `GAME_ADDR` | `:8080` | HTTP/WebSocket 监听地址 |
| `GAME_DB` | `data/game.db` | SQLite 路径；`:memory:` 使用内存库 |
| `GAME_TOKEN` | 空 | WebSocket Bearer token；为空时关闭鉴权，仅限本地开发 |

## WebSocket

连接 `GET /v1/game?room_id=1`，请求头：

```text
Device-Id: <设备唯一 ID>
Client-Id: <客户端 ID>
Protocol-Version: 1|2|3
Authorization: Bearer <GAME_TOKEN>
```

首帧必须是 `hello`：

```json
{"type":"hello","version":3,"transport":"websocket","audio_params":{"format":"opus","sample_rate":16000,"channels":1,"frame_duration":60}}
```

服务端返回 `hello` 后进入房间。设备发送的 `listen` 消息不参与服务端状态控制；房间发言权由 ASR 的 `speech_started` 抢占，首个发言者确定后其他设备立即禁麦。音频帧按 `Protocol-Version` 解析，服务端复用现有小智 v1/v2/v3 Opus 帧格式。

服务端只下发三类控制消息：

- `{"type":"tts","state":"start"}`：禁麦或开始播放。
- `{"type":"tts","state":"sentence_start","text":"..."}`：当前朗读文本。
- `{"type":"tts","state":"stop"}`：播放结束，设备按固件播放队列排空后恢复收音。

当前房间规则：每房最多 6 台设备；每局 10 分钟；单人发言最长 60 秒；局中加入的设备本轮只收听，下一局自动获得发言资格；猜中或超时后朗读固定汤底并自动开始下一局。

## 实现边界

当前 `cmd/server` 使用本地桩实现启动服务：

- `DebugASR`：只识别测试用的 `say:<text>` 字节，不接真实语音。
- `SilenceTTS`：返回静音 PCM，不产生 AI 语音。
- `RuleJudge`：用关键词和汤底包含关系判题，不调用大模型。

真实 Qwen ASR、主持 LLM、TTS 和 Linx 设备入口尚未接入。协议和房间 actor 已完整可测，但不要把当前二进制当作可用的线上语音服务。

## 验证

```bash
cd server
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
GOCACHE=/tmp/codex-staticcheck-cache \
GOMODCACHE=/tmp/codex-staticcheck-mod \
staticcheck ./...
```

测试覆盖协议帧、Opus 编解码、SQLite 题库、双设备抢麦、局中加入限制、判题播报、自动下一局和发言者离场后的恢复收音。
