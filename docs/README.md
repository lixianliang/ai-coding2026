# 文档索引

项目文档按四个主题组织：

- [商业价值](business-value.md)：客户、价值、成本、定价假设、门店验证和风险。
- [产品设计](product-design.md)：游戏形态、玩家流程、规则、积分、功能边界和演示脚本。
- [架构设计](architecture.md)：Linx、XiaoZhi 固件、Go 游戏服务、MCP、Game WSS 和语音状态机。
- [部署与使用](deployment.md)：配置、启动、固件/Linx 接入、Game WSS、玩家操作和验证命令。
- [AI 工程效率](ai-engineering-efficiency.md)：模型基准、真实链路 E2E、自动化测试、工程门禁和研发 skills。
- [Demo 视频](demo/turtle-soup-demo.mp4)：实际设备录制的游戏演示。

代码入口：

- `server/`：Go 游戏服务端；
- `xiaozhi-esp32/`：XiaoZhi ESP32 固件及海龟汤设备扩展；
- `server/config.example.json`：不含密钥的配置模板。
