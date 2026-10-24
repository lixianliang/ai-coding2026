# 海龟汤 Go 服务端

固定房间 `1/2/3` 的单设备海龟汤 MVP：每房一台 `yunliao-s3`，玩家围坐共享设备与 AI 主持人；玩家按 BOOT 开始收音，再按一次结束。设备继续使用现有 `hello`、`listen`、`tts` 控制消息和 Opus 音频格式。

设备固件基于开源项目 `xiaozhi-esp32`，普通设备服务由 Linx 提供：Linx 负责 OTA、设备激活、入口对话和设备本地 MCP。`self.game.start(room_id)` 被 Linx 智能体调用并返回受理结果后，设备关闭 Linx 音频连接，按 `room_id` 连接本 Go 服务的 Game WSS。Go 服务是独立的游戏服务端，不提供 XiaoZhi 通用对话；入房后先播放欢迎提示并等待玩家说“开始游戏”，不自动出题。游戏连接结束后设备回到 idle，后续唤醒再重新连接 Linx。

```text
设备 Opus ──► Go 解码 PCM ──► 豆包 ASR 2.0（说话人分离）
                                      │
                                      ├─ final text ──► DeepSeek 判题
                                      │
设备 Opus ◄── Go 编码 PCM ◄── MiniMax TTS ◄┘
```

## 启动

```bash
cd server
cp config.example.json config.local.json
chmod 600 config.local.json
# 填写 asr/llm/tts 的 api_key 后启动
go run ./cmd/server
```

所有运行配置都放在一个 JSON 中；`GAME_CONFIG` 只负责指定文件，默认 `config.local.json`。`config.local.json` 已被 `.gitignore` 排除，不要提交真实密钥。配置了 `questions` 后，它表示完整启用题库：同 `id` 覆盖汤面和汤底，未出现的旧题会在下次启动时停用。

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `address` | `:8080` | HTTP/WebSocket 监听地址 |
| `database` | `data/game.db` | SQLite 路径；`:memory:` 使用内存库 |
| `device_token` | 空 | 设备 WebSocket Bearer token；空值关闭鉴权，仅限本地开发 |
| `turn_duration_seconds` | `60` | 单次收音上限 |
| `game_duration_seconds` | `600` | 单局上限 |
| `questions` | 内置题库 | 完整启用的题库，每项包含 `id`、`surface`、`answer`；省略时使用内置题 |
| `asr` / `llm` / `tts` | 见示例 | ASR 厂商 / DeepSeek / MiniMax 地址、模型和密钥 |

## 游戏规则

- 每房一台设备；固定房间 `1/2/3`，第二台设备连接返回 `room is full`。
- 每局 10 分钟，单次收音最多 60 秒。
- Game WSS 入房后先播放欢迎提示，不自动开局；玩家按 BOOT 说“开始游戏”后才朗读汤面。
- AI 播报题面后，玩家之间可以直接讨论；只有需要向 AI 提问、猜测汤底或发出继续/退出指令时，BOOT 第一次按发送 `listen state=start mode=manual`，第二次按发送 `listen state=stop`。
- 游戏进行中如果忘记汤面，可以说“再说一遍汤面”或“重复一下汤面”；服务端重播当前固定题面，不进入判题。
- `listen start` 前和 `listen stop` 后的音频丢弃；普通现场讨论不上传，只有 `stop` 才提交本次 manual 收音给 ASR。
- 服务端用 ASR final 文本交给 DeepSeek 判定：`yes/no/close/irrelevant/correct`。
- 判定和固定汤底都由 MiniMax 流式合成；先攒够 5 帧（300 ms）PCM 再起播，后续分片边到边编码成 Opus，按 60 ms/帧下发；短回复在合成结束时完整 Flush，尾帧全部发完才发送 `tts stop`。每段播报独立创建 Opus 编码器，避免跨房间/跨播报共享预测状态；播报最长等待 2 分钟，与判题超时分开。固定汤底文本来自题库，不由 LLM 改写。
- AI 播报时设备处于 `speaking`，忽略 BOOT；`tts stop` 后保持 `idle`。
- 猜中或超时后朗读汤底并进入讨论态，不自动开下一局；按 BOOT 说“游戏继续”才开新题，也可以说“开始游戏”。
- 讨论态语音不做判题；说“退出游戏”关闭连接并释放房间。
- 玩家之间现场直接交流，服务端不接收也不转发玩家音频；采用豆包 ASR 说话人分离；不做独立声纹注册、AI 玩家、真人主持和掉线恢复。

## 积分与结算页

- 玩家直接提问，不必报名字。豆包 ASR 返回会话内说话人编号，Go 按本局首次提问顺序映射为“玩家 1、2、3”；判题模型只返回 verdict。无法识别唯一说话人时照常判题，不计个人分。
- 积分：问出“是” +1，接近 +2，猜中汤底 +5；同分时猜中者优先，再按首次发言顺序。
- 局终设备只朗读汤底和冠军；完整排名看结算页 <http://127.0.0.1:8080/scoreboard?room_id=1>。
- 数据接口：`GET /v1/rooms/{room_id}/scoreboard`，只读、不需要 token。

## 设备协议

连接 `GET /v1/game?room_id=1`，请求头：

```text
Device-Id: <设备唯一 ID>
Client-Id: <客户端 ID>
Protocol-Version: 1|2|3
Authorization: Bearer <device_token>
```

首帧必须是 `hello`，服务端返回 `hello` 后入房并播放欢迎提示；玩家说“开始游戏”后才朗读汤面。设备上传 `listen` 和二进制 Opus 帧。服务端复用 TTS 控制：

- `{"type":"tts","state":"start"}`：设备进入播报态，不收麦。
- `{"type":"tts","state":"sentence_start","text":"..."}`：当前朗读文本。
- `{"type":"tts","state":"stop"}`：播放结束；manual 模式下设备回到 idle。

不使用 `waiting/playing/play_done` 等新状态。

## 浏览器链路测试

启动服务后打开 <http://127.0.0.1:8080/playground>。页面录音后重采样为 16 kHz、单声道、s16le PCM，调用：

```text
POST /v1/playground/turn
Content-Type: application/octet-stream

<raw PCM>
```

返回 ASR 文本、判题结果、回复文本，以及可直接播放的 PCM。浏览器麦克风只允许在 `localhost` 或 HTTPS 页面使用。若配置了 `device_token`，在页面输入同一 token。

## 评估

判题回归：

```bash
go run ./cmd/eval -config config.local.json
```

完整语音链路（TTS 生成测试语音 → 配置的 ASR → DeepSeek 判题 → MiniMax TTS）：

```bash
go run ./cmd/eval -config config.local.json -e2e
```

当前评估集为同一题目的 8 条样例，覆盖 `yes/no/close/irrelevant/correct` 五类及带姓名表述；质量函数为“调用无错误且实际判定等于预期”，任一失败返回非零退出码。2026-10-24 云端语音链曾测得 8/8、总耗时 72.28 秒；这是包含输入合成和实时上传的历史运行记录，不是当前版本保证或玩家首音时延。

统一质量体系见 [技术方案第 10 节](../docs/design/turtle-soup-game-simplified.md#10-评分维度与现场证据)：模型基准、真实链路 E2E、单元/集成与工程检查、AI 研发 skills。`-e2e` 是云端组件链，不经过设备或完整游戏状态机；说话人验证使用下方 `cmd/speaker-eval`，完整真机游戏另外验收。

## 检查

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
GOCACHE=/tmp/codex-staticcheck-cache GOMODCACHE=/tmp/codex-staticcheck-mod \
  /tmp/codex-go-bin/staticcheck ./...
```

测试覆盖入房欢迎、“开始游戏”、manual start/stop、start 前音频丢弃、第二设备拒绝、猜中后朗读固定汤底、讨论态不判题、“游戏继续”、“退出游戏”、60 秒收音超时、10 分钟局时超时、配置题库更新与停用，以及 TTS 流式首片下发、非整帧分片重组、尾帧完整下发和异常中断。

## 验证边界

- 已验证云端 Qwen ASR、DeepSeek 判题、MiniMax TTS 和 `/playground` HTTP 链路。
- 自动化测试已验证 Game WSS 状态流程：欢迎 → “开始游戏” → 汤面 → 错误回答 → 猜中并朗读汤底 → 讨论态 → “游戏继续” → “退出游戏”。
- 已通过 Linx 本地 MCP 将真机切到 Game WSS；固件确认 8 MB PSRAM、ES8388、LCD 和 Wi-Fi 正常。
- 真机 TTS 完整播放与 BOOT 全流程需在音频节奏修复后复验。
- 当前不做掉线快照恢复；设备断开即离房。

### ASR 自动区分玩家

配置示例已改用豆包 ASR 2.0：`asr.provider=doubao`，`app_id` 填应用 ID，`api_key` 填 Access Token。密钥只放忽略的 `config.local.json`。旧配置不填 `provider` 时仍用 Qwen，能判题，但没有说话人编号，不计个人分。无需改固件。

- 采用官方 `bigmodel_async` 接口，开启 `enable_nonstream`、`enable_speaker_info`、`show_utterances`，`ssd_version=200`。
- 每个设备连接复用一条 ASR 连接；BOOT stop 后补静音等待确定分句，再把整次提问交给裁判；如果确定文本早于按键结束到达，使用缓存结果，不等待静音期间不会再推送的新事件。发言中间的停顿不会提前判题。
- 主持播报和玩家讨论期间只发合成静音保活，不采集现场讨论。保持会话有额外 ASR 时长成本，不能再按仅提问时长估算费用。
- 玩家编号只保证本局映射，不代表实名声纹；换局重新编号，断线不恢复。无说话人/混合说话人不计个人分。录音回放目前尚未实现。

真实云端说话人回归（PCM 为 16 kHz、单声道、s16le；至少两个声音，包含同一人再次发言）：

```bash
go run ./cmd/speaker-eval -config config.local.json \
  -pcm /tmp/a1.pcm,/tmp/b1.pcm,/tmp/a2.pcm -speakers A,B,A -pause 5s
```

此命令检查跨提问身份一致性和不同声音不合并；不能替代设备上多名真人的识别准确率验收。
