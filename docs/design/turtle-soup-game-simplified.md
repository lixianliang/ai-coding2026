# 海龟汤：Linx + Go 精简方案

> 一台 `yunliao-s3` 放在桌上，多人围坐共玩；玩家现场直接交流，按 BOOT 向 AI 主持人提问。固定房间 1/2/3，每局 10 分钟。当前只做线下共设备模式，不做多设备、声纹、AI 玩家、真人主持和掉线恢复。

## 1. 方案结论

### 1.1 上游固件与服务端关系

`xiaozhi-esp32` 是开源的 XiaoZhi ESP32 固件项目，负责设备端的音频采集、Opus 编解码、播放、按键、网络连接和 MCP 工具执行。本仓库不重新实现一套设备固件，而是在该开源项目上通过一个小范围 PR 增加海龟汤所需的能力：注册 `self.game.start(room_id)`、切换 Game WSS 地址，以及将游戏收音切换为 manual start/stop 模式。

这里的 Linx 可以理解为 XiaoZhi 固件配置连接的服务端。固件通常通过 OTA 下发或保存 Linx 的 WebSocket 配置，Linx 负责设备激活、OTA、唤醒后的普通入口对话和设备端 MCP 调用。玩家说“进入 2 号房玩海龟汤”时，Linx 智能体识别意图并调用设备本地 MCP；MCP 返回受理结果后，设备关闭当前 Linx 音频连接，使用临时 URL 连接独立的 Go Game WSS。

Linx 与 Go 游戏服务是两个独立的服务端角色：Linx 负责 XiaoZhi 的通用设备服务和入口对话，Go 只负责进入指定房间后的题目、收音、ASR、判题、TTS 和房间状态。Game WSS 不替换固件，也不要求 Linx 承载游戏期间的连续音频；游戏连接结束后，设备回到 idle，下一次唤醒再按 XiaoZhi 的配置重新连接 Linx。

```text
ESP32-S3（yunliao-s3）
  ├─ Linx WSS：OTA、唤醒、入口对话、设备本地 MCP
  └─ Game WSS：manual 收音、AI 主持、Opus 音频
          │
          ▼
       Go 单服务
       ├─ 固定房间 1/2/3
       ├─ 题库、局时、讨论态、固定汤底
       ├─ Qwen Realtime ASR（manual commit + 中间结果）
       ├─ DeepSeek 判题
       └─ MiniMax TTS + Opus/PCM 编解码
```

设备同一时间只连一条语音链路：

```text
玩家：进入 2 号房玩海龟汤
        ↓
Linx 智能体调用设备本地 MCP：self.game.start(room_id=2)
        ↓
设备关闭 Linx 连接，连接 Game WSS：/v1/game?room_id=2
        ↓
Go 将设备加入 2 号房并播放欢迎提示
        ↓
玩家按 BOOT 说“开始游戏”，AI 朗读汤面
        ↓
AI 播报题面后，玩家现场讨论；需要向 AI 提问或猜测汤底时按 BOOT 开始收音，再按一次结束
        ↓
说“退出游戏”或连接断开
        ↓
设备回到 idle；下一次唤醒重新走 Linx
```

不做匹配或排队大厅，也不让云端 MCP 承载连续音轨。Linx 只负责把玩家带进游戏；Go 入房后先播放欢迎提示并等待“开始游戏”，再进入题目状态并维护后续游戏流程。

## 2. 房间与规则

- 固定房间 1、2、3；房间号由 Linx 入口对话选择，并注入智能体提示词。
- 每房一台共享设备，玩家数量按现场空间决定；玩家提问时先口头报名字（“我是小明，……”），用于个人积分，不做声纹。
- 房间状态只在 Go 内存中保存；设备断开即离房，不做断线快照恢复。
- 每局 10 分钟；每次提问最长 60 秒，到时由服务端收麦。
- Game WSS 入房后先播放欢迎提示，不自动开局；玩家说“开始游戏”后才选题并朗读汤面。
- AI 播报题面后，设备处于 idle；玩家之间的普通现场讨论不启动设备收音，也不提交给服务端。
- 玩家需要向 AI 提问、猜测汤底，或在讨论态发出“游戏继续”“退出游戏”等控制指令时，按 BOOT 发送 `listen state=start mode=manual`；说完后再按一次发送 `listen state=stop`。
- 只有一次 manual start 与 stop 之间的音频进入 ASR；stop 后 Go 等待 final 文本，再交给判题或讨论态指令处理。
- 猜中或超时后朗读固定汤底，进入讨论态，不自动开下一局。
- 讨论态的普通交流不做判题；只有按 BOOT 提交的语音才会被识别，其中“游戏继续”等指令才开新一局。
- 欢迎提示会明确给出“开始游戏 / 游戏继续 / 退出游戏”三个高频指令。
- “退出游戏”由 Game WSS 的 ASR 识别；Go 关闭连接并释放房间。

Go 的房间 actor 串行处理加入、离开、收音、最终文本、AI 判定、TTS 完成和计时器，避免语音回调与局状态互相覆盖。

## 3. 协议：直接复用小智现有消息

Game WSS 不定义第二套状态机，只复用现有小智 WebSocket 格式：

- 连接后设备发 `hello`，服务端返回 `hello`。
- 设备发 `listen state=start mode=manual`，随后上传 Opus 二进制帧。
- 设备发 `listen state=stop` 后，服务端提交 Qwen ASR。
- 服务端发 `tts state=start`，随后下发 Opus 二进制帧，最后发 `tts state=stop`。
- `stt`、`tts sentence_start` 只用于显示，不参与控制。

房间从 Game WSS URL 的 `room_id` 查询参数确定；设备身份取握手头 `Device-Id`。房间的“开局、判题、讨论、结束”只存在于服务端，不下发成设备协议。

| 方向 | 消息 | 含义 |
| --- | --- | --- |
| 设备 → Go | `hello` | 声明音频参数 |
| 设备 → Go | `listen start/stop` | BOOT 开始/结束一次向 AI 提问、猜测汤底或控制指令 |
| 设备 → Go | 二进制 Opus | 当前 manual 收音片段；普通现场讨论不上传 |
| Go → 设备 | `tts start` | 进入播报态，设备不收麦 |
| Go → 设备 | 二进制 Opus | AI 主持音频 |
| Go → 设备 | `tts stop` | 本次播放结束；manual 模式下设备回 idle |
| Go → 设备 | `stt` / `tts sentence_start` | 字幕与显示，可省略 |

控制约定只有三条：

1. 播放：`tts start` → Opus 帧 → `tts stop`。
2. 设备在 AI 播报时处于 `speaking`，BOOT 不启动新一轮收音。
3. `tts stop` 不负责截断播放；固件等待解码队列排空后完成状态切换。

不使用自定义 `waiting/playing/play_done` 消息，也不需要服务端接收“播放完成”。

## 4. 语音流程

### 4.1 开局

1. 设备连接 Game WSS 后，Go 先播放欢迎提示并停在等待开局状态，不自动选题。
2. 玩家按 BOOT 说“开始游戏”，Go 从可配置题库选一题，锁住汤面和汤底。
3. 服务端发 `tts start`，用 AI 音色朗读汤面并提示“提问时请先报名字”，再发 `tts stop`。
4. 设备播完后回到 idle；玩家可以现场讨论，只有需要向 AI 提问或猜测汤底时才按 BOOT 开始一次 manual 收音。

### 4.2 玩家提问

1. 玩家需要向 AI 提问或猜测汤底时按 BOOT，设备发 `listen start` 并上传 Opus。
2. Go 将 manual 收音的 Opus 解码为 PCM，持续送入 Qwen Realtime ASR。
3. Qwen 的 partial 结果只用于日志或显示，不推进游戏状态。
4. 玩家说完后再按 BOOT，设备发 `listen stop`；Go 提交当前 ASR 音频并等待 final。
5. final 文本交给 DeepSeek，判定为 `yes / no / close / irrelevant / correct`，同时提取玩家开头自报的名字并累计积分（见 4.5）。
6. Go 将判定结果交给 TTS，编码为 Opus 下发；播放完成后设备回 idle，玩家可以继续现场讨论。

不自己实现 VAD。当前按键 manual 模式由玩家显式按键结束收音；只有按键确认的语音才提交给 Go，ASR 中间结果由 Qwen 提供。

### 4.3 结束与讨论

1. 玩家猜中或局时到，AI 依次播报：“猜对了 / 时间到”、本局冠军、固定汤底、“本轮游戏结束”，并提示“想开新一局请说游戏继续”；完整排名只在结算页展示。
2. 本局进入讨论态，不自动开下一局；玩家可继续现场讨论，普通讨论不会进入 ASR 或判题。
3. 玩家按 BOOT 提交“游戏继续”或“开始游戏”等明确指令后，服务端才开始新一局。
4. 玩家按 BOOT 提交“退出游戏”后，服务端关闭 Game WSS 并释放房间，设备回 idle。

### 4.4 音频格式

- 设备 ↔ Go：Opus，16 kHz、单声道，沿用固件现有编解码。
- Go 内部与 ASR/TTS provider：PCM 16 kHz、单声道、16-bit。
- TTS 返回 PCM，Go 编成 Opus 下发。
- 首版不接收或转发玩家之间的音频。

### 4.5 积分与结算页

**玩家身份：** 不做声纹。玩家每次提问先报名字，判题模型在同一次调用里输出 `{"verdict":..., "player":"小明"}`；没报名字的提问照常判题，但不计分。

**积分规则：**

| 判定 | 积分 |
| --- | --- |
| `yes`（问出“是”） | +1 |
| `close`（接近） | +2 |
| `correct`（猜中汤底） | +5 |
| `no` / `irrelevant` | 0 |

排名按积分降序；同分时猜中者优先，再按首次发言顺序。没有人得分时不产生冠军。积分只在本局有效，新一局重新计算；成绩保存在房间 actor 内存中，不落库。

**设备播报：** 局终播报“猜对了。本局冠军是小明，得6分。汤底是：……。本轮游戏结束。想开新一局请说“游戏继续”。”，不朗读完整排名；超时则以“时间到。”开头。

**结算页：** Go 服务提供网页，门店用电视或平板打开即可，不需要 App：

- `GET /scoreboard?room_id=1`：结算页，每 2 秒轮询一次，可切换 1/2/3 号房。
- `GET /v1/rooms/{room_id}/scoreboard`：只读数据接口，返回统一 `{code, message, reqid, data}`。

`data` 字段：

| 字段 | 说明 |
| --- | --- |
| `status` | `idle` 未开局 / `playing` 进行中 / `finished` 本局结束 |
| `surface` | 汤面 |
| `answer` | 汤底，仅 `finished` 时返回 |
| `champion` | 冠军名字，仅 `finished` 且有人得分时返回 |
| `players` | 按名次排序：`name`、`score`、`questions`、`yes`、`close`、`correct` |

进行中显示实时积分，局终显示冠军与汤底；下一局开始前保留上一局结果。结算页是门店大屏的只读展示，不要求设备 token。

## 5. AI 主持

AI 只做主持，不拥有答案：

- 朗读汤面。
- 判断玩家问题与固定答案的关系：`yes / no / close / irrelevant / correct`，并提取玩家自报的名字。
- 识别玩家是否猜中，组织简短播报。
- 结束时由服务端取固定汤底，交给 TTS 逐字朗读。

固定汤底不能由模型改写。判题输出使用结构化结果，规则命中后由 Go 推进房间，不让模型直接控制房间状态。

## 6. 服务端

一个 Go 进程即可：

- Gin：`/healthz`、Game WSS 升级、`/playground` 联调页。
- GORM + SQLite：题库、汤底、局记录；`config.local.json` 的 `questions` 定义完整启用题库，运行中的房间状态放内存。
- 每房一个 actor：当前设备、局状态、截止时间、讨论态。
- Qwen Realtime ASR：每台在线设备一个会话，manual commit。
- 主持智能体：汤面、判题、结束播报。
- TTS + Opus：生成 AI 音频并下发给当前设备。

不做大厅、排队服务、跨进程消息总线、重连快照和复杂协议网关。

## 7. 固件改动

1. `mcp_server.cc` 注册 `self.game.start`，参数 `room_id` 限定为 1、2、3。
2. `application.cc/.h` 增加 `StartGame`；关闭 Linx 后连接 Game WSS。
3. `WebsocketProtocol` 支持临时 URL override，不覆盖 NVS 中的 Linx 配置。
4. 游戏连接切换到 `kListeningModeManualStop`，BOOT 控制一次提问的开始与结束。
5. Game WSS 地址由 `CONFIG_TURTLE_SOUP_GAME_URL` 注入；本地验证可指向局域网地址，真实部署使用可达的 WSS 域名。

设备只保留基础连接状态，房间与游戏阶段全部由服务端维护。ES8388、Opus 编解码、AEC 和分区表不需要为 MVP 改动。

## 8. 首版范围

要做：

- Linx 入口、设备本地 MCP、Game WSS 切换。
- 房间 1/2/3、共享设备、入房欢迎、显式开始、manual 收音、讨论态、确认后开下一局。
- AI 出题、实时语音提问、AI 判定、固定汤底 TTS。
- 60 秒单次提问、10 分钟局时。
- 自然语言继续/退出游戏。

不做：

- 多设备房间、玩家语音转发、声纹、说话人分离。
- AI 玩家、真人主持。
- 匹配/排队大厅、积分、排行榜、多人混音。
- 掉线快照恢复、断线续局。

## 9. 产品与商业价值

**产品定位：给桌游店、剧本杀店增加一个无需店员主持的语音加购项目。**

- 一台设备放在桌上，玩家围坐直接交流，不装 App、不拿手机。
- 店里复用闲置包间和换场时间，AI 主持不增加排班。
- 卖点是完整体验：现成设备、题库、自动主持、自动判题、持续运营。
- 付费假设需用真实门店数据验证：设备采购、房间/月订阅或按局计费。

详细定位与商业假设见 [turtle-soup-product-positioning.md](turtle-soup-product-positioning.md)。

## 10. 评分维度与现场证据

| 评分维度 | 评委要看到 | 现场证据 |
| --- | --- | --- |
| 商业价值 & 产品设计 50% | 谁付费、为什么持续用、现场是否好玩 | 桌游店换场场景；一台真机开局；玩家现场自然讨论；完整玩完一题；展示单局成本、第二局率和回本假设 |
| 架构设计 20% | 设备薄、服务端权威、协议简单 | Linx 入口与 Game WSS 分工；固定房间；复用 `hello/listen/tts/Opus`；没有自定义 waiting 状态机 |
| AI 价值呈现 15% | AI 真正承担主持人工作 | 自然语言选房/继续/退出；理解不同问法并判定；固定汤底由 AI 音色朗读 |
| AI 工程效率 15% | AI 参与研发且有可重复回归 | 判题评测、完整语音链路评测、manual 协议自动测试、硬件构建/烧录 skill、Go 测试与 provider 调试 skill |

## 11. 3 分钟 Demo

| 时间 | 演示内容 |
| --- | --- |
| 0:00–0:20 | 桌游店换场空房；店主想多卖一局，但不想增加主持排班 |
| 0:20–0:45 | 玩家唤醒设备说“进入 1 号房玩海龟汤”；Linx 触发设备本地 MCP，设备播放欢迎提示，玩家按 BOOT 说“开始游戏”后 AI 朗读汤面 |
| 0:45–1:35 | 玩家围坐讨论；需要向 AI 提问或猜测汤底时按 BOOT 进行 manual 收音，不同问法得到“是 / 不是 / 接近” |
| 1:35–2:05 | 一名玩家猜中，AI 朗读固定汤底并提示可以继续讨论 |
| 2:05–2:30 | 玩家按 BOOT 说“游戏继续”开新一局；再演示“退出游戏” |
| 2:30–3:00 | 展示单局成本、完整完成率、第二局率和门店回本假设 |

现场要证明：玩家之间直接交流，设备只承担与 AI 主持人的问答；整局没有店员讲题或判题。

## 12. 主要风险

- 共享一台设备意味着同一时间只有一名玩家向 AI 提问；现场讨论不受影响。
- 玩家身份只靠口头报名字：忘记报名不计分，冒用他人名字无法识别；不做声纹是首版范围控制。
- 门店网络和 4G 稳定性决定 ASR/TTS 是否可用，必须现场实测。
- Qwen 的中间结果可能因噪声或口音波动；manual stop 只提交一次最终结果，仍需评测集回归。
- 设备断线直接离房，当前局不恢复；这是首版明确接受的限制。
- 单局云成本、设备维护成本和门店付费意愿必须用真实数据验证。

## 参考

- [Linx OTA](https://linx.qiniu.com/docs/xrobot/platform/OTA)
- [Linx WebSocket 协议](https://linx.qiniu.com/docs/xrobot/platform/websocket)
- [Linx 设备端 MCP](https://linx.qiniu.com/docs/xrobot/mcp/hardware-mcp)
- [Qwen Realtime ASR API](https://help.aliyun.com/zh/model-studio/qwen-asr-realtime-api/?spm=a2c4g.11186623.help-menu-2400256.d_2_6_3_0_3.620e3274Dn7gft)
