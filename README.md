# ai-coding2026

ESP32-S3 多人语音海龟汤：1～6 个真人玩家、AI 主持、无按键、每局 10 分钟。

- Linx 负责 OTA、唤醒、入口对话和设备本地 MCP；用户说“我要玩海龟汤”后进入游戏。
- Go 服务负责房间 1/2/3、题库、游戏状态、发言权和 TTS。
- 多人房间另走一条 Game WSS，传 Opus 音频；MCP 不承载连续音轨或跨设备广播。
- 服务端内部统一 PCM 16 kHz、单声道、16-bit；设备网络链路继续 Opus，不改 ES8388/AEC。
- 当前只做海龟汤，不做 AI 玩家、真人主持和掉线恢复。

简化方案、评分展示和 3 分钟 Demo：[docs/design/turtle-soup-game-simplified.md](docs/design/turtle-soup-game-simplified.md)。

产品定位与商业设计：[docs/design/turtle-soup-product-positioning.md](docs/design/turtle-soup-product-positioning.md)。

同事原方案：[docs/design/turtle-soup-game.md](docs/design/turtle-soup-game.md)。
