# 海龟汤项目交付说明

一台 ESP32-S3 设备担任 AI 主持，供桌游店、剧本杀店的一桌玩家共玩。玩家现场讨论，按 BOOT 向 AI 提问；AI 讲题、判题、揭晓固定汤底，网页展示积分。默认每局 10 分钟，玩家决定是否继续下一局。

## 1. 交付清单

| 要求 | GitHub 中的位置 | 状态 |
| --- | --- | --- |
| 产品源代码 | [Go 服务与网页](../server/)、[设备固件](../xiaozhi-esp32/) | 已纳入仓库 |
| 运行说明 | 本文第 2 节、[服务端 README](../server/README.md) | 已整理 |
| 架构、模块规格、分工 | [技术方案](design/turtle-soup-game-simplified.md)，模块与分工见第 13 节 | 已整理 |
| 商业价值与产品设计 | [产品方案](design/turtle-soup-product-positioning.md) | 包含定价与门店试点假设 |
| 质量评估与回归 | [技术方案第 10 节](design/turtle-soup-game-simplified.md#10-评分维度与现场证据) | 区分已有证据和待验收能力 |
| Demo 视频 | [录制与上传说明](demo/README.md) | **待录制、待上传** |

仓库包含本产品的 Go、HTML/JavaScript 和设备 C/C++ 源码；前端由 Go 内嵌提供，无独立前端服务。Linx、豆包、DeepSeek、MiniMax 是外部平台，需自行开通账号和配置权限，其服务端源码不属于本项目。固件基于 XiaoZhi，保留[上游许可证](../xiaozhi-esp32/LICENSE)和依赖声明；不能把上游固件全部算作团队原创。

## 2. 从源码运行

### 2.1 服务端与测试页面

```bash
git clone https://github.com/lixianliang/ai-coding2026.git
cd ai-coding2026
```

按[服务端启动说明](../server/README.md#启动)安装 Go、C 编译器和 Opus 原生库，再执行：

```bash
cd server
cp config.example.json config.local.json
chmod 600 config.local.json
# 编辑 config.local.json 后再启动
go mod download
go run ./cmd/server
```

本地 JSON 必填：豆包 `asr.app_id`、`asr.api_key`（Access Token），DeepSeek `llm.api_key`，MiniMax `tts.api_key`；模型、音色及资源权限需与自己的账号匹配。`GAME_CONFIG` 可指定其他 JSON 路径；默认相对当前工作目录读取 `config.local.json`。

| 入口 | 用途 |
| --- | --- |
| `http://127.0.0.1:8080/healthz` | HTTP 健康检查，成功为 `code=0` |
| `http://127.0.0.1:8080/playground` | 浏览器录音，验证 ASR → 判题 → TTS；只测固定示例题的一次问答，不是整局游戏 |
| `http://127.0.0.1:8080/scoreboard?room_id=1` | 设备所在房间的实时积分、局终汤底与排名 |
| `ws://HOST:8080/v1/game` | 设备游戏连接基础地址，固件追加 `room_id` |

题库配置在 `questions`，每题包含 `id/surface/answer`。重启服务后更新启用题库；数据库默认在 `server/data/game.db`，运行中的房间和积分在内存中，重启不恢复。修改题库或重启前先结束正在进行的游戏。

### 2.2 yunliao-s3 设备

硬件：ESP32-S3、8 MB PSRAM、ES8388、带 BOOT 键。演示优先用与电脑互通的 Wi-Fi；USB 用于供电、烧录和日志，不代替设备网络。

1. 安装 ESP-IDF，推荐 `release/v6.1`，最低 v6.0.1；按安装位置加载 `export.sh`。
2. 在 [Linx 智控台](https://xrobo.qiniu.com/)建立入口智能体、绑定设备；按 [OTA 文档](https://linx.qiniu.com/docs/xrobot/platform/OTA)设置 Linx OTA 地址。不要沿用上游默认小智 OTA 地址当作 Linx 地址。
3. 入口智能体的约定：用户要玩海龟汤时确认房间 1/2/3，调用设备本地 `self.game.start(room_id)`；题面与判题交给 Go。绑定后确认平台能发现这个设备工具，无需额外实现一套云端游戏 MCP。
4. 回到仓库根目录，配置 Go 地址并构建：

```bash
cd xiaozhi-esp32
. /path/to/esp-idf/export.sh
cp main/boards/yunliao-s3/config.json main/boards/yunliao-s3/config.local.json
```

编辑这个**本地副本**的 `sdkconfig_append`，保留已有 AEC、多行显示配置，追加：

```json
"CONFIG_TURTLE_SOUP_GAME_URL=\"ws://HOST:8080/v1/game\""
```

把 `HOST` 换成设备可访问的电脑局域网地址，不能填 `localhost` 或 `127.0.0.1`。Linx OTA 配置也只写本地副本，使用平台提供的实际地址。板型 JSON 的 `type` 不用于设置服务器地址。构建脚本会重新生成 `sdkconfig`，不要只修改生成文件。

```bash
python3 scripts/build.py yunliao-s3 -c config.local.json --name yunliao-s3 --language zh-CN
idf.py -p PORT flash
idf.py -p PORT monitor
```

`PORT` 换成当前 USB 串口，monitor 用 `Ctrl+]` 退出。本地副本不提交 Git。已有正确固件时，无需为了每次录 Demo 都烧录。更多排障见[硬件 skill](../.agents/skills/xiaozhi-yunliao-s3-firmware/SKILL.md)。

当前 `StartGame` 给游戏连接传空 token，因此同网演示使用示例中的空 `device_token`；仅在服务端填 token 会导致设备鉴权失败。公网部署需要补齐设备游戏 token 注入及 TLS 入口；当前 Go 直接提供 HTTP/WS，不能只把 URL 改为 `wss://` 就认为已启用 TLS。

### 2.3 跑一局

1. 唤醒：“你好小智”，等设备响应，再说“进入一号房玩海龟汤”。
2. 听完欢迎语；按 BOOT，说“开始游戏”，再按 BOOT 结束收音。
3. 听完汤面；玩家现场讨论。提问时均采用“按一下 → 一人说话 → 再按一下”。不必报名字。
4. 可以问“再说一遍汤面”；重播不触发判题和加分。
5. 猜中后听完整汤底，在同房间结算页核对排名；先讨论，不应自动开新局。
6. 按键说“游戏继续”开始下一局；按键说“退出游戏”离房。

AI 播报时按键不开始收音。设备报连接失败时，先确认服务启动、电脑地址没变、同网互通、8080 端口可达和固件地址正确。

## 3. 验证和交付依据

在 `server/` 下运行：

```bash
go test ./...
go test -race ./...
go vet ./...
gofmt -l .
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
```

格式检查应无输出；staticcheck 的安装与临时缓存命令见 [PR skill](../.agents/skills/quick-pr/SKILL.md)。不需要云密钥的自动化测试，与需要真实账号的模型评测分开运行。

`cmd/eval` 是 8 条判题基准；`-e2e` 加上真实语音组件，但不经过设备和整局状态机。说话人 A→B→A 回归命令、硬件验收边界见[服务端 README](../server/README.md#asr-自动区分玩家)。视频应展示真机整局，不能用浏览器单轮成功代替。

本次交付整理仅核对源码、Issue/PR 和文档链接，不新增“真机整局通过”的声明。既有结果及待补齐项统一见技术方案第 10 节。

## 4. Demo 与最终提交

先按[录制脚本](demo/README.md)完成真机整局，再制作约 3 分钟展示版。录好后将 MP4 与本页、根 README 中的视频入口一起通过 `codex/*` 分支和 PR 提交。评委应能从仓库首页直接打开视频；目前视频项尚未完成。
