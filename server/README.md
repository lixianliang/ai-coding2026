# 海龟汤 Go 服务端

固定房间 `1/2/3` 的海龟汤 MVP：AI 出题、服务端 VAD/ASR、AI 判题、TTS 播报、房间内语音转发。设备继续使用现有 `hello`、`listen`、`tts` 控制消息和 Opus 音频格式。

```text
设备 Opus ──► 服务端解码 PCM ──► Qwen Realtime ASR（自带 VAD）
                                      │
玩家语音 Opus ──► 房间其他设备          ├─ final text ──► DeepSeek 判题
                                      │
设备 Opus ◄── 编码 PCM ◄── MiniMax TTS ◄┘
```

## 启动

```bash
cd server
cp config.example.json config.local.json
chmod 600 config.local.json
# 填写 asr/llm/tts 的 api_key 后启动
go run ./cmd/server
```

所有运行配置都放在一个 JSON 中；`GAME_CONFIG` 只负责指定文件，默认 `config.local.json`。`config.local.json` 已被 `.gitignore` 排除，不要提交真实密钥。

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `address` | `:8080` | HTTP/WebSocket 监听地址 |
| `database` | `data/game.db` | SQLite 路径；`:memory:` 使用内存库 |
| `device_token` | 空 | 设备 WebSocket Bearer token；空值关闭鉴权，仅限本地开发 |
| `turn_duration_seconds` | `60` | 单人发言上限 |
| `game_duration_seconds` | `600` | 单局上限 |
| `asr` / `llm` / `tts` | 见示例 | Qwen / DeepSeek / MiniMax 地址、模型和密钥 |

## 游戏规则

- 每房最多 6 台设备；固定房间 `1/2/3`。
- 每局 10 分钟，每次发言最多 60 秒；不固定轮流，谁先被 Qwen VAD 检测到起音谁获得发言权。
- 发言者的 Opus 音频实时转发给房间内其他设备；其他有资格设备收到 `tts start` 后闭麦。
- ASR final 文本交给 DeepSeek 判定：`yes/no/close/irrelevant/correct`。
- 判定结果和固定汤底都由 MiniMax 合成，编码成 Opus 下发给房间设备。
- 局中加入的设备本轮只收听，下一局开始后获得发言资格。
- 猜中或超时后朗读汤底并自动开始下一局；说“退出游戏”关闭当前设备连接。
- 房间状态只在内存中维护，不做掉线快照恢复。

## 设备协议

连接 `GET /v1/game?room_id=1`，请求头：

```text
Device-Id: <设备唯一 ID>
Client-Id: <客户端 ID>
Protocol-Version: 1|2|3
Authorization: Bearer <device_token>
```

首帧必须是 `hello`，服务端返回 `hello` 后入房。设备上传 `listen` 和二进制 Opus 帧。服务端复用三类 TTS 控制：

- `{"type":"tts","state":"start"}`：禁麦或开始播放。
- `{"type":"tts","state":"sentence_start","text":"..."}`：当前朗读文本。
- `{"type":"tts","state":"stop"}`：播放结束，固件在播放队列排空后恢复收音。

## 浏览器链路测试

启动服务后打开 <http://127.0.0.1:8080/playground>。页面录音后重采样为 16 kHz、单声道、s16le PCM，调用：

```text
POST /v1/playground/turn
Content-Type: application/octet-stream

<raw PCM>
```

返回 ASR 文本、判题结果、回复文本，以及可直接播放的 PCM：

```json
{
  "code": 0,
  "data": {
    "transcript": "这碗汤和他妈妈有关吗？",
    "verdict": "yes",
    "speech": "是。",
    "audio_pcm_base64": "...",
    "sample_rate": 16000,
    "channels": 1,
    "bits_per_sample": 16
  }
}
```

浏览器麦克风只允许在 `localhost` 或 HTTPS 页面使用。若配置了 `device_token`，在页面输入同一 token。

## 评估

判题回归：

```bash
go run ./cmd/eval -config config.local.json
```

完整语音链路（TTS 生成测试语音 → Qwen ASR → DeepSeek 判题 → MiniMax TTS）：

```bash
go run ./cmd/eval -config config.local.json -e2e
```

评估集覆盖 `yes/no/close/irrelevant/correct` 五类。本次实测两条命令均为 `5/5 passed`，完整链路约 20～30 秒；结果会随网络和模型服务波动。

## 检查

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
staticcheck ./...
```

单元测试覆盖协议帧、Opus 编解码、SQLite 题库、房间抢麦、候场、判题播报、自动下一局、离场恢复和 MiniMax hex 音频解码。

## 验证边界

- 已验证云端 Qwen ASR、DeepSeek 判题、MiniMax TTS 和 `/playground` HTTP 链路。
- 未验证 ESP32 真机音频、Linx 真机 MCP 切流和公网 4G 环境。
- 当前不做掉线快照恢复；设备断开即离房。
