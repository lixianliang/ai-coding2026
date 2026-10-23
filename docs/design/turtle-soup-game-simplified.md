# 海龟汤：Linx + Go 精简方案

> 一台设备、一桌玩家、一个 AI 主持人。固定房间 1/2/3；每局 10 分钟；单次收音最多 60 秒；玩家按 BOOT 手动开始/结束发言。

## 1. 架构

```text
ESP32-S3（yunliao-s3）
  ├─ Linx WSS：OTA、入口对话、设备本地 MCP
  └─ Game WSS：海龟汤问答与 TTS
          │
          ▼
       Go 单服务
       ├─ 房间 1/2/3（每房一台设备）
       ├─ 题库、局时、收音状态、固定汤底
       ├─ Qwen Realtime ASR（manual commit）
       ├─ DeepSeek 判题
       └─ MiniMax TTS + Opus/PCM 编解码
```

```text
玩家说“进入 2 号房玩海龟汤”
  → Linx 智能体调用设备本地 MCP：self.game.start(room_id=2)
  → 设备关闭 Linx 连接
  → 连接 Game WSS：/v1/game?room_id=2
  → Go 加入 2 号房并朗读汤面
  → 玩家按 BOOT 提问、再按 BOOT 结束
  → AI 判定并朗读结果
  → 猜中/超时后讨论；说“游戏继续”开新局，或说“退出游戏”回 Linx
```

Linx 负责入口，不承载连续音轨。Go 负责房间和游戏状态。首版没有游戏大厅、跨设备音频转发和玩家身份识别。

## 2. 房间与操作

- 固定房间 1、2、3，房号由 Linx 入口选择并注入提示词。
- 每个房间同时只允许一台设备；第二台连接返回 `room is full`。
- 房间状态只在 Go 内存中维护，设备断开即离房；首版不做掉线恢复。
- 每局 10 分钟；单次收音最多 60 秒，超时由服务端结束本次收音。
- BOOT 第一次按：进入 `listening`，发送 `listen state=start mode=manual`。
- BOOT 第二次按：发送 `listen state=stop`，服务端提交 ASR 并判题。
- AI 播报时设备处于 `speaking`，按键忽略；`tts stop` 后回到 `idle`，下一次提问需要再次按键。
- 猜中或超时后进入讨论态，不自动开下一局。玩家按 BOOT 说“游戏继续”才开新题。
- 说“退出游戏”关闭 Game WSS，设备回 Linx。
- 首版不做声纹、说话人分离、AI 玩家、真人主持和掉线快照恢复。

## 3. 协议

Game WSS 直接复用现有小智消息，不定义第二套状态机：

| 方向 | 消息 | 含义 |
| --- | --- | --- |
| 设备 → Go | `hello` | 声明 Opus / 16 kHz / 单声道音频参数 |
| 设备 → Go | `listen state=start mode=manual` | 玩家按下 BOOT，开始收音 |
| 设备 → Go | 二进制 Opus | 收音期间的麦克风音频 |
| 设备 → Go | `listen state=stop` | 玩家再次按下 BOOT，提交本次音频 |
| Go → 设备 | `tts state=start` | 进入播报态，设备不收麦 |
| Go → 设备 | `tts state=sentence_start,text=...` | 显示主持文本 |
| Go → 设备 | 二进制 Opus | 汤面、判定或汤底音频 |
| Go → 设备 | `tts state=stop` | 播报结束；manual 模式回到 idle |

关键约束：

1. `listen start` 之前收到的音频直接丢弃。
2. 只有 `listen stop` 才调用 Qwen ASR commit；不做客户端 VAD，也不等待静音。
3. 服务端不扩展 `waiting / playing / play_done`；播放结束只复用 `tts stop`。
4. 固定汤底由 Go 作为文本传给 MiniMax TTS，DeepSeek 不负责改写汤底。
5. 玩家现场交谈不经过设备；服务端不接收也不转发玩家之间的音频。

## 4. 游戏状态

服务端只维护以下业务阶段：

```text
hosting           AI 正在朗读汤面或判定
awaiting_question 设备可按键提问
listening         设备正在上传一次提问
judging           等待 DeepSeek 判题
finished          已公布汤底，允许讨论
```

`listen start` 只在 `awaiting_question` 或 `finished` 被接受。`finished` 下的语音只识别“游戏继续”和“退出游戏”，不进入判题。

## 5. 语音处理

```text
设备 Opus
  → Go 解码 PCM 16 kHz/mono/s16le
  → Qwen Realtime ASR append
  → listen stop 触发 commit
  → final transcript
  → DeepSeek verdict: yes/no/close/irrelevant/correct
  → Go 生成固定回复
  → MiniMax TTS PCM
  → Go 编码 Opus
  → 设备 tts start / sentence_start / Opus / stop
```

- 网络链路保持 Opus，不改 ES8388 和现有固件音频链路。
- 服务端内部统一 PCM，便于 ASR、判题和 TTS 对接。
- 页面的 60 秒收音上限和服务的 60 秒 turn timer 同时生效。
- 固定汤底用 AI 音色朗读，但文本由题库锁定。

## 6. 代码落点

| 模块 | 职责 |
| --- | --- |
| `xiaozhi-esp32/main/boards/yunliao-s3/yunliao_s3.cc` | BOOT manual 开麦/收麦；播报态忽略按键 |
| `server/internal/game/manager.go` | 单设备房间、计时、状态推进、继续/退出 |
| `server/internal/game/asr_qwen.go` | Qwen ASR append/commit/manual final |
| `server/internal/game/judge_deepseek.go` | 按汤面和固定汤底判题 |
| `server/internal/game/tts_minimax.go` | 主持语音合成 |
| `server/internal/api/router.go` | Game WSS、鉴权、浏览器测试页 |
| `server/cmd/eval` | 判题评测与完整语音链路回归 |

入场 MCP 和 Game WSS 切换属于固件集成项；当前 Go 服务端可独立通过 `/playground` 和 `/v1/game` 验证。

## 7. 验证与回归

自动回归：

```text
listen start 前音频不进入 ASR
listen stop 后才产生 final 并判题
同一房间第二台设备被拒绝
猜中后朗读固定汤底
讨论态不判题
说“游戏继续”开新题
说“退出游戏”释放房间
60 秒收音超时和 10 分钟局时超时
```

真实链路：

```bash
cd server
go run ./cmd/eval -config config.local.json       # 5/5 passed, 3.6s
go run ./cmd/eval -config config.local.json -e2e  # 5/5 passed, 19.8s
go test ./...
go test -race ./...
```

固件已构建并烧录到 `yunliao-s3`，启动日志确认板型、8 MB PSRAM 和 ES8388 初始化成功。当前设备未保存 Wi-Fi，停留在配网模式；BOOT 上行、Game WSS、Linx MCP 切流和 4G 尚未完成真机验证，不能写成已跑通。

## 8. 3 分钟 Demo 与评分

| 评分维度 | 评委要看到 | 现场证据 |
| --- | --- | --- |
| 商业价值 & 产品设计 50% | 门店为什么买、为什么不是手机 | 换场空档一台设备开局；无店员主持；现场讨论不经过设备；展示第二局率和回本假设 |
| 架构设计 20% | 设备够薄、服务端够稳 | 一张图讲清 Linx 入口和 Game WSS；单设备房间；复用 `hello/listen/tts/Opus`；无 waiting 状态机 |
| AI 价值呈现 15% | AI 真正主持 | Qwen 听懂问题、DeepSeek 判题、MiniMax 朗读汤面和固定汤底 |
| AI 工程效率 15% | AI 参与研发并可回归 | 判题评测集、真实语音 e2e、manual 按键状态回归、一条命令验证 |

| 时间 | 演示内容 |
| --- | --- |
| 0:00–0:20 | 门店换场空档，一台设备放到桌上 |
| 0:20–0:40 | 按 BOOT 说进入 1 号房；AI 朗读汤面 |
| 0:40–1:35 | 玩家围坐讨论，按 BOOT 向 AI 提问，听取“是/不是/接近” |
| 1:35–2:05 | 猜中后 AI 朗读固定汤底，进入讨论 |
| 2:05–2:30 | 按 BOOT 说“游戏继续”；演示“退出游戏” |
| 2:30–3:00 | 展示成本、完成率、第二局率和自动回归结果 |

## 9. 主要风险

- 玩家现场讨论直接进行，不需要声纹；首版也不声称能识别具体提问者。
- 共享设备同一时间只能有一路 AI 提问，按键时序必须清晰。
- Qwen/DeepSeek/MiniMax 任一云端服务抖动都会影响一局体验。
- Linx 入口和 Game WSS 切换未接通前，设备只能验证 manual 收音和独立服务端。
- 设备断线直接离房；需要门店运营时再加恢复策略。
