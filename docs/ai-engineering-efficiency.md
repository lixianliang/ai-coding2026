# AI 工程效率与质量回归

AI 在这个项目里的作用包括：定义可计算的验收标准、生成和维护测试样例、定位真实语音链路问题、补充回归测试，并把硬件/provider/提交经验沉淀为可重复使用的研发资产。

## 1. 四层质量体系

| 层次 | 核心问题 | 当前入口 | 结果 |
| --- | --- | --- | --- |
| 模型基准 | 判题是否符合人工预期？ | `server/cmd/eval` | 8 条固定样例覆盖 `yes/no/close/irrelevant/correct`；失败返回非零 |
| 真实链路 E2E | TTS、ASR、LLM、TTS 能否真实串起来？ | `server/cmd/eval -e2e`、`server/cmd/speaker-eval` | 验证云端语音链和会话内说话人一致性；不替代真机验收 |
| 自动化测试与工程门禁 | 改动是否破坏协议、状态、音频和并发？ | `server/internal/*/*_test.go`、Go 检查命令 | 单测、WebSocket 集成测试、race、vet、staticcheck、gofmt |
| AI 研发资产 | 相同问题能否快速复现、修复和交付？ | `AGENTS.md`、`.agents/skills/`、PR | 形成“问题 → 回归 → 修复 → PR”闭环 |

四层分别覆盖模型质量、云端可用性、代码正确性和研发效率，不用单个总分掩盖断音、误判或状态错误。

## 2. 评估函数

### 2.1 判题正确率

```text
A = 正确 verdict 的样例数 / 样例总数
```

调用失败、超时、解析失败和 verdict 不等于人工标注都计为失败。评测集应按 `yes/no/close/irrelevant/correct` 分类，逐例记录预期、实际结果、错误和耗时。

当前基准是同一题目的 8 条样例，适合做版本回归，不代表不同题目和真实玩家的总体准确率。扩充评测集时保留独立样例，避免用调过提示词的样例证明泛化能力。

### 2.2 云端链路通过率

```text
E = 完成 TTS → ASR → LLM → TTS 且判定正确的案例数 / 案例总数
```

`-e2e` 检查真实 provider 是否返回可用结果和预期 verdict。它不经过 ESP32、Opus 播放、BOOT 和完整游戏状态机，因此不能替代设备验收。

说话人回归使用同一 ASR 会话发送 `A → B → A` 等标注音频：

```text
S：同一标签保持同一 speaker_id；不同标签不能共用 speaker_id
```

命令和 PCM 要求见[部署与使用](deployment.md)。合成语音通过不代表三名真人在噪声环境中一定准确。

### 2.3 游戏和音频完整性

游戏规则使用确定性断言：

```text
G = 通过的游戏规则断言数 / 规则断言总数
```

重点断言包括：欢迎后不自动开局、一次 manual start/stop 只提交一次、重播不判题、讨论不自动下一局、继续/退出状态正确、第二设备被拒绝、积分归属正确。

音频流使用边界断言：

- TTS 分片重组后 PCM 内容完整；
- 首段预缓冲后才起播；
- 60 ms Opus 帧按节奏发送；
- 非整帧尾部补齐且不丢尾帧；
- 每段播报使用独立编码状态；
- provider 中断时不能报告成功。

这些测试可以证明协议和数据边界正确，不能单独证明设备扬声器没有破音；音质仍需要目标设备现场试听。

### 2.4 时延和成本

记录以下阶段，不把 TTS 生成时间和玩家感知时延混成一个数字：

- `stop → ASR final`；
- `stop → 首个可播放 TTS 音频`；
- `tts start → tts stop`；
- 一局总时长。

上线前按 P50/P95 统计，并同时记录 ASR 秒数、TTS 计费字符和 LLM token。豆包连接可能用静音保活，ASR 成本不能只按玩家实际说话秒数估算。

## 3. 可执行回归

在 `server/` 目录执行：

```bash
# 确定性代码回归
go test ./...
go test -race ./...
go vet ./...
gofmt -l .

# staticcheck 使用仓库外临时缓存
GOBIN=/tmp/codex-go-bin GOPROXY=https://proxy.golang.org,direct \
  go install honnef.co/go/tools/cmd/staticcheck@latest
GOCACHE=/tmp/codex-staticcheck-cache GOMODCACHE=/tmp/codex-staticcheck-mod \
  /tmp/codex-go-bin/staticcheck ./...

# 真实模型基准与语音链路，需要本地配置和云端密钥
go run ./cmd/eval -config config.local.json
go run ./cmd/eval -config config.local.json -e2e
go run ./cmd/speaker-eval -config config.local.json \
  -pcm /tmp/a1.pcm,/tmp/b1.pcm,/tmp/a2.pcm -speakers A,B,A -pause 5s
```

自动化测试使用 fake provider 和 `httptest`，不应在普通单测中调用真实云服务。真实 provider 质量和延迟使用 `cmd/eval`；完整设备验收还要现场确认 Linx MCP 切流、Game WSS、BOOT、ES8388 播放、屏幕和网络。

## 4. AI 研发闭环

每次问题按以下顺序处理：

1. 保存能复现问题的日志、输入或协议片段，不提交密钥、录音、设备标识和本地配置。
2. 把现象转成一个确定性断言或评测样例。
3. 用最小修改修复共享根因。
4. 运行对应层级的回归；provider 问题再运行真实云端命令，硬件问题再做目标设备验收。
5. 在 PR 中写清触发条件、改动、验证命令、实际结果和未覆盖边界。
6. 把可复用的排障步骤更新到 skill，避免下一次重新摸索。

仓库已有的研发资产：

- [`go-test-engineering`](../.agents/skills/go-test-engineering/SKILL.md)：选择单测、WebSocket 集成测试、race 和 provider 评测层级；
- [`go-provider-debugging`](../.agents/skills/go-provider-debugging/SKILL.md)：排查 ASR、说话人、LLM、TTS、Opus 和首音问题；
- [`xiaozhi-yunliao-s3-firmware`](../.agents/skills/xiaozhi-yunliao-s3-firmware/SKILL.md)：构建、烧录、串口、Game WSS 和真机验收；
- [`quick-pr`](../.agents/skills/quick-pr/SKILL.md)：限定改动范围、执行检查、走 `codex/*` 分支和 PR；
- [`AGENTS.md`](../AGENTS.md)：规定模块边界、错误处理、日志、协议和提交约束。

Skills 是执行方法，测试输出才是质量证据。评审时可沿着“问题日志 → 回归用例 → 修复代码 → PR 检查结果”查看 AI 参与研发的完整链路。

## 5. 现场展示证据

3 分钟 Demo 中可以展示一条真实闭环：

1. 设备完成入房、显式开始、提问、汤面重播、猜中和结算；
2. 结算页显示本局结果；
3. 终端展示一次真实基准或 E2E 输出；
4. 展示一个历史问题对应的回归测试和修复 PR。

现场展示应标注命令执行日期、代码提交 SHA、模型配置版本和样例数量。历史结果、合成语音结果、浏览器联调结果和真机结果分开标注，不能互相替代。
