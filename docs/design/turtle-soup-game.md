# 海龟汤：Linx + Go 精简方案

> 1～6 个真人玩家，AI 主持；房间 1/2/3；每局 10 分钟。当前只做海龟汤，不做 AI 玩家、真人主持、掉线快照恢复。

## 1. 方案结论

海龟汤拆成两条链路：

```text
ESP32-S3（yunliao-s3）
  ├─ Linx WSS：OTA、唤醒、入口对话、设备本地 MCP
  └─ Game WSS：房间语音、AI 主持、Opus 音频
          │
          ▼
       Go 单服务
       ├─ 房间 1/2/3（每房一个 actor）
       ├─ 题库、局时、发言权、固定汤底
       ├─ Qwen Realtime ASR（服务端 VAD）
       ├─ 主持智能体与问答判定
       └─ TTS、Opus/PCM 音频路由
```

设备同一时间只连一条语音链路：

```text
玩家：我要进入 2 号房玩海龟汤
        ↓
Linx 智能体调用设备本地 MCP：self.game.start(room_id=2)
        ↓
设备回复工具结果，关闭 Linx 连接
        ↓
连接 Game WSS：/v1/game?room_id=2
        ↓
Go 将设备加入 2 号房，开局或候场
        ↓
游戏结束 / 玩家说“退出游戏”
        ↓
关闭 Game WSS，设备重新连接 Linx
```

没有游戏大厅，也不让云端 MCP 承载连续音轨。Linx 负责把玩家带进游戏，Go 负责房间内发生的一切。

## 2. 房间与规则

- 固定房间 1、2、3；玩家在入口对话中选房，房号注入 Linx 智能体提示词。
- 每房 1～6 个真人，每个玩家一台设备。
- 房间状态只在 Go 内存中保存；设备断开即移出。首版不做断线恢复。
- 每局 10 分钟；每次发言最长 60 秒；到时由服务端收麦并推进。
- 不设固定轮流。每个有效提问是一轮，谁先起音谁获得本轮发言权。
- 局中再加入的设备本轮只能听，下一局开始才获得发言资格。
- 一局结束后，仍在房间的设备自动进入下一局，使用新汤面。
- “退出游戏”由 Game WSS 的 ASR 识别；Go 关闭该设备的 Game WSS，设备回到 Linx。

Go 的房间 actor 串行处理加入、离开、`speech_started`、`speech_stopped`、最终文本、AI 判定、TTS 完成和计时器，避免多人同时说话时出现两份发言权。

## 3. 协议：直接复用小智现有消息

Game WSS 不定义第二套状态机，只复用现有小智 WebSocket 格式：

- 连接后设备发 `hello`，服务端返回 `hello`。
- 设备发 `listen state=start mode=auto`，随后上传 Opus 二进制帧。
- 服务端发 `tts state=start`，随后下发 Opus 二进制帧，最后发 `tts state=stop`。
- `stt`、`tts sentence_start` 只用于显示，不参与控制。

房间从 Game WSS URL 的 `room_id` 查询参数确定；设备身份取握手头 `Device-Id`，权限由 Go 校验。房间的“开局、候场、判定、结束”只存在于服务端，不下发成设备协议。

| 方向 | 消息 | 含义 |
| --- | --- | --- |
| 设备 → Go | `hello` | 声明音频参数 |
| 设备 → Go | `listen start/stop` | 设备开始/结束送音 |
| 设备 → Go | 二进制 Opus | 当前设备麦克风音频 |
| Go → 设备 | `tts start` | 进入播放态；`speaking` 时设备不收麦；可以没有音频 |
| Go → 设备 | 二进制 Opus | AI TTS 或房间内其他玩家的音频 |
| Go → 设备 | `tts stop` | 本次音频发送结束；设备播完队列后恢复收音 |
| Go → 设备 | `stt` / `tts sentence_start` | 字幕与显示，可省略 |

控制约定只有三条：

1. 禁麦：发 `tts start`，不必有音频。
2. 播放：`tts start` → Opus 帧 → `tts stop`。
3. 开麦：先 `tts start`，再 `tts stop`；即使中间没有音频也能完成状态切换。

关键点：`tts stop` 不负责立即截断播放。固件在 `kListeningModeAutoStop` 下会等播放队列排空后再开麦，因此服务端不需要接收“播放完成”，也不需要自定义 `waiting / playing / play_done` 消息。

## 4. 语音流程

### 4.1 开局

1. Go 从固定答案题库选一题，锁住汤面和汤底。
2. 向房间设备发 `tts start`，用 AI 音色朗读汤面，再发 `tts stop`。
3. 有资格的设备进入可收音状态；局中加入的设备保持 `tts start` 禁麦。

### 4.2 玩家提问

1. 所有有资格设备的 Opus 都送进各自的 Qwen Realtime ASR。
2. Qwen 的 `speech_started` 到达时，房间 actor 原子选第一台设备获得发言权。
3. 其余设备立即收到 `tts start`，关闭麦克风。
4. 发言设备的音频实时转发给房间扬声器设备；服务端同时保留音频喂 ASR。
5. partial 结果只用于屏幕显示，不释放发言权。
6. Qwen 的 `speech_stopped` 到达后，等待 final，进入 AI 判定。
7. AI 判定完成，先播判定结果，再让有资格设备恢复下一提问。

不自己实现 VAD。设备只持续送 Opus，起音、句尾、中间结果都由 Qwen Realtime ASR 提供。首版只让一台房间扬声器设备外放，其他设备负责收音和显示，避免多台喇叭叠音和跨设备回声。

### 4.3 音频格式

- 设备 ↔ Go：Opus，16 kHz、单声道，沿用固件现有编解码。
- Go 内部：PCM 16 kHz、单声道、16-bit。
- 玩家音频直接转发原 Opus，不为转发重复解码/编码。
- TTS 返回 PCM，Go 编成 Opus 下发。

## 5. AI 主持

AI 只做主持，不拥有答案：

- 朗读汤面。
- 判断玩家问题与固定答案的关系：`yes / no / close / irrelevant / correct`。
- 识别玩家是否猜中，组织简短播报。
- 结束时用 AI 音色逐字朗读服务端保存的固定汤底。

固定汤底不能由模型改写。判题输出使用结构化结果，规则命中后由 Go 推进房间，不让模型直接控制房间状态。

## 6. 服务端

一个 Go 进程即可：

- Gin：`/healthz`、Game WSS 升级。
- GORM + SQLite：题库、汤底、局记录；运行中的房间状态放内存。
- 每房一个 actor：房间成员、当前局、资格集合、发言权、截止时间。
- Qwen Realtime ASR：每台活跃设备一个会话，直接使用服务端 VAD。
- 主持智能体：汤面、判定、结束播报。
- TTS + Opus：生成 AI 音频并下发给房间扬声器。

不做大厅、排队服务、跨进程消息总线、重连快照和复杂协议网关。

## 7. 固件改动

只需四处：

1. 在 `mcp_server.cc` 注册 `self.game.start`，参数 `room_id` 限定为 1、2、3。
2. 在 `application.cc/.h` 增加游戏连接状态和启动/退出切换；MCP 先返回“已接受”，再关闭 Linx、连接 Game WSS。
3. 让 `WebsocketProtocol` 支持调用方传入 Game WSS URL/token，游戏连接复用同一套 `hello`、`listen`、`tts` 和 Opus 实现。
4. 游戏模式切到 `kListeningModeAutoStop`。`yunliao-s3` 开启 AEC 后默认是 `kListeningModeRealtime`，不切会在 `speaking` 状态下继续开麦。

设备连接状态只保留：

```text
XIAOZHI_CONNECTED
GAME_STARTING
GAME_CONNECTED
GAME_EXITING
```

`GAME_WAITING / GAME_IN_ROOM` 都是服务端业务状态，不是设备状态。ES8388 驱动、Opus 编解码、AEC 和分区表不需要为 MVP 改动。

## 8. 首版范围

要做：

- Linx 入口、设备本地 MCP、Game WSS 切换。
- 房间 1/2/3、1～6 个真人、候场、自动下一局。
- AI 出题、实时语音提问、AI 判定、固定汤底 TTS。
- Qwen 服务端 VAD、60 秒单次发言、10 分钟局时。
- 房间内 Opus 转发、自然语言退出游戏、回到 Linx。

不做：

- AI 玩家、真人主持。
- 游戏大厅、积分、排行榜、多人混音。
- 掉线快照恢复、断线续局。
- 每台设备同时外放；首版只选一台房间扬声器。

## 9. 产品与商业价值

**产品定位：给桌游店/剧本杀店增加一个 15 分钟、无需店员主持的语音加购项目。**

- 玩家坐下即可玩，不装 App、不拿手机、不需要店员讲规则。
- 店内提高空房和换场时间的利用率；AI 主持不增加排班。
- 卖点不是“接了一个大模型”，而是完整体验：现成设备、现成题库、自动主持、自动判题、可连续运营。

首版商业假设，需用真实 BOM 和门店数据修正：

| 项目 | 假设 |
| --- | --- |
| 三台设备起步套件 | ¥1,999 |
| 第 4～6 台扩容 | ¥499/台 |
| 内容订阅 | ¥99/间/月，30 道题 |
| 门店售价 | ¥29.9/间/15 分钟 |
| 云端成本 | ≤¥0.5/局 |

单店每天多卖 1 局约 ¥777/月增量收入，扣除订阅后约 3 个月回本；每天 2 局约 1.4 个月。先验证这一个数，不先扩功能和硬件。

## 10. 评分维度与现场证据

| 评分维度 | 评委要看到 | 现场证据 |
| --- | --- | --- |
| 商业价值 & 产品设计 50% | 谁付费、为什么持续用、现场是否好玩 | 桌游店换场场景；三台真机一分钟开局；无手机、无店员；完整玩完一题；展示套件价、单局价、回本假设和单局成本 |
| 架构设计 20% | 多房间可信、设备够薄、协议够简单 | 一张图讲清 Linx 入口与 Game WSS 音频面；服务端权威房间状态；复用 `hello/listen/tts/Opus`；没有自定义 waiting 状态机 |
| AI 价值呈现 15% | AI 不是套壳 TTS | 自然语言选房和退出；理解不同问法并判定；AI 主持连续追问；固定汤底由 AI 音色朗读 |
| AI 工程效率 15% | AI 真参与研发且可验证 | 30 条问答评测集；一条命令回归；PCM 回放；展示 ASR→判定→TTS 延迟、单局成本和服务端日志 |

## 11. 3 分钟 Demo

| 时间 | 演示内容 |
| --- | --- |
| 0:00–0:15 | 桌游店换场空房；店主想多卖一局，但不想增加主持排班 |
| 0:15–0:35 | 三台设备上桌，玩家说“进入 2 号房玩海龟汤”；Linx 触发设备本地 MCP，AI 朗读汤面，不使用手机 |
| 0:35–1:35 | 三名玩家连续提问；不同问法得到“是 / 不是 / 接近”；一人说话时其他设备自动闭麦；房间扬声器只出一路声音 |
| 1:35–2:00 | 第四台设备中途加入，本轮只能听；一名玩家猜中，AI 朗读固定汤底 |
| 2:00–2:20 | 玩家说“退出游戏”，设备回到 Linx 正常对话 |
| 2:20–3:00 | 展示套件价、单局售价、回本区间、单局成本，以及评测集和 ASR→TTS 延迟 |

必须现场证明两件事：玩家语音先被同房间其他人听到，AI 随后才判定；整局没有店员、没有手机参与。

## 12. 主要风险

- **最大风险**：Linx 只负责入口，不承载多人连续音轨；多人实时语音必须走自建 Game WSS。
- 4G 抖动会影响实时转发和 ASR；需要 Opus 帧缓冲和超时收麦。
- 所有有资格设备保持开麦会产生多路 Qwen 会话；3～6 台的并发、带宽和成本必须实测。
- Qwen VAD 可能被环境噪声误触发；抢麦必须在 `speech_started` 时原子完成。
- 多台设备同时外放会啸叫；首版固定一台房间扬声器，离开时再切换。
- 设备断线直接离房，当前局不恢复；这是首版明确接受的限制。
- 设备入口最终必须落到设备本地 MCP 并切换 WSS。仅靠 Linx 云端 MCP 或提示词，不能建立房间音频链路。

## 参考

- [Linx OTA](https://linx.qiniu.com/docs/xrobot/platform/OTA)
- [Linx WebSocket 协议](https://linx.qiniu.com/docs/xrobot/platform/websocket)
- [Linx 设备端 MCP](https://linx.qiniu.com/docs/xrobot/mcp/hardware-mcp)
- [Linx 云端 MCP](https://linx.qiniu.com/docs/xrobot/mcp/software-mcp)
- [Qwen Realtime ASR](https://help.aliyun.com/zh/model-studio/qwen-asr-realtime)
