# 部署与使用

## 组件

部署需要三部分：

1. `yunliao-s3` 固件，包含 `self.game.start` 和 Game WSS 切换能力；
2. Linx XiaoZhi 服务，负责设备激活、OTA、普通对话和 MCP；
3. 本仓库的 Go 游戏服务，负责房间、题库、ASR、判题、TTS 和结算页。

设备一次只连接 Linx 或 Go Game WSS。游戏退出或连接断开后，设备回到 idle；下一次唤醒时按固件原有配置重新连接 Linx。

## Go 服务配置

```bash
cd server
cp config.example.json config.local.json
chmod 600 config.local.json
# 填写 asr、llm、tts 的密钥
go run ./cmd/server
```

`GAME_CONFIG` 可指定配置文件路径，默认使用 `config.local.json`：

```bash
GAME_CONFIG=/etc/turtle-soup/config.json go run ./cmd/server
```

运行配置只放在本地文件中。`server/config.local.json` 已加入 `.gitignore`，禁止提交真实密钥；`server/data/` 的 SQLite 数据也不会提交。

主要配置项：

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `address` | `:8080` | HTTP/WebSocket 监听地址 |
| `database` | `data/game.db` | SQLite 路径；`:memory:` 适合测试 |
| `device_token` | 空 | Game WSS 的 Bearer token；空值仅适合本地开发 |
| `turn_duration_seconds` | `60` | 单次 manual 收音上限 |
| `game_duration_seconds` | `600` | 单局上限 |
| `questions` | 内置题库 | 完整启用题库；每项包含 `id`、`surface`、`answer` |
| `asr` | 豆包或 Qwen | ASR provider、地址、模型和密钥 |
| `llm` | DeepSeek | 判题服务地址、模型和密钥 |
| `tts` | MiniMax | 播报服务地址、模型、音色和密钥 |

配置 `questions` 后，它表示完整启用的题库：同一 `id` 会覆盖汤面和汤底，配置中未出现的旧题会在下次启动时停用。

Qwen Realtime ASR 的备用配置可参考[阿里云实时语音识别 API 文档](https://help.aliyun.com/zh/model-studio/qwen-asr-realtime-api/?spm=a2c4g.11186623.help-menu-2400256.d_2_6_3_0_3.620e3274Dn7gft)。

## 固件和 Linx 配置

固件通过 `CONFIG_TURTLE_SOUP_GAME_URL` 配置 Go 服务地址，例如：

```text
wss://game.example.com
```

设备调用 `self.game.start(room_id)` 时，固件自动追加 `room_id` 查询参数。认证信息和普通 XiaoZhi/Linx 地址由设备配置管理；用户不能通过语音传入服务地址或 token。

Linx 侧需要让智能体理解类似“进入 2 号房玩海龟汤”的意图，并调用设备 MCP：

```json
{
  "name": "self.game.start",
  "arguments": {"room_id": 2}
}
```

设备返回“已接受启动请求”后才异步切换 Game WSS。这个响应不代表 Go 已经入房成功。

## Game WSS 接入

服务地址：

```text
GET /v1/game?room_id=1
```

请求头由固件自动发送：

```text
Device-Id: <设备唯一 ID>
Client-Id: <客户端 ID>
Protocol-Version: 1|2|3
Authorization: Bearer <device_token>
```

HTTP 接口：

- `GET /healthz`：健康检查；
- `GET /playground`：浏览器联调页；
- `POST /v1/playground/turn`：上传 16 kHz、单声道、16-bit PCM 做单次链路测试；
- `GET /scoreboard?room_id=1`：结算页；
- `GET /v1/rooms/{room_id}/scoreboard`：结算数据接口，不需要设备 token。

## 玩家使用方式

1. 对 Linx 说“进入 1 号房玩海龟汤”。
2. 听到欢迎语后按 BOOT，说“开始游戏”。
3. AI 播报汤面；玩家围坐讨论。
4. 需要提问或猜汤底时按 BOOT，说完再按一次 BOOT。
5. 听 AI 判定；需要重听时按 BOOT 说“再说一遍汤面”。
6. 猜中或超时后，可按 BOOT 说“游戏继续”；结束时说“退出游戏”。

## 开发检查

```bash
cd server
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
GOCACHE=/tmp/codex-staticcheck-cache GOMODCACHE=/tmp/codex-staticcheck-mod \
  /tmp/codex-go-bin/staticcheck ./...
```

判题基准和云端语音链路：

```bash
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
go run ./cmd/speaker-eval -config config.local.json \
  -pcm /tmp/a1.pcm,/tmp/b1.pcm,/tmp/a2.pcm -speakers A,B,A -pause 5s
```

`-e2e` 只验证云端 TTS → ASR → LLM → TTS，不等于完整设备验收。完整设备验收还需要确认 Linx MCP 切换、Game WSS 入房、BOOT 收音、TTS 播放和退出后恢复 Linx。

## 验证边界

- 自动化测试覆盖欢迎、开始、manual start/stop、重播、判题、积分、继续、退出和超时。
- 已验证 Game WSS 的 Opus 分片、TTS 预缓冲、尾帧发送和配置题库更新。
- 真机验证需要使用目标 `yunliao-s3`，检查网络、ES8388、屏幕、BOOT 和完整音频播放。
- 当前不做掉线快照恢复；设备断开即离房。
