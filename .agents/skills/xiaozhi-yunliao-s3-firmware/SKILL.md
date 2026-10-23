---
name: xiaozhi-yunliao-s3-firmware
description: Build, flash, monitor, and debug the XiaoZhi ESP32-S3 yunliao-s3 device used by this repository. Use for board-specific firmware changes, Game WSS URL injection, provisioning, boot, display, ES8388 audio, PSRAM, or physical acceptance; not for server-only or unrelated ESP32 work.
metadata:
  short-description: Yunliao S3 firmware build, flash, and device acceptance
---

# Yunliao S3 firmware

## Board facts

- Firmware root: `<repo>/xiaozhi-esp32`
- Build variant: `yunliao-s3`
- Board config: `main/boards/yunliao-s3/config.json`
- MCU: ESP32-S3, 8 MB octal PSRAM, 16 MB flash
- Audio: ES8388, duplex I2S, device AEC enabled
- Wake word: `你好小智` by default
- Do not persist MAC, UUID, Wi-Fi credentials, tokens, LAN IPs, or live logs in the repository.

Detect the current serial port instead of reusing an old number:

```bash
find /dev -maxdepth 1 \( -name 'cu.usbmodem*' -o -name 'cu.wchusbserial*' -o -name 'cu.SLAB_USBtoUART*' \) -print
```

## Build

ESP-IDF v6.1 is installed at `/Users/chenfeifan/esp/release/v6.1/esp-idf`; activate it in every fresh shell:

```bash
cd <repo>/xiaozhi-esp32
. /Users/chenfeifan/.espressif/tools/activate_idf_v6.1.sh
python scripts/build.py yunliao-s3 --name yunliao-s3 --language zh-CN
```

`build.py` regenerates `sdkconfig`, so make board/config changes before the final clean build, not between compile and flash. Use `--build-options-json` only for semantic options declared by the board; it does not accept arbitrary Kconfig symbols.

### Local Game WSS URL

`CONFIG_TURTLE_SOUP_GAME_URL` must remain empty in committed config. For a local device test, create an untracked board config, append the current reachable endpoint, and select it with `-c`:

```json
{
  "type": "yunliao-s3",
  "target": "esp32s3",
  "builds": [{
    "name": "yunliao-s3",
    "sdkconfig_append": [
      "CONFIG_USE_DEVICE_AEC=y",
      "CONFIG_TURTLE_SOUP_GAME_URL=\"ws://HOST:8080/v1/game\""
    ]
  }]
}
```

```bash
cp main/boards/yunliao-s3/config.json main/boards/yunliao-s3/config.local.json
# Edit the local copy.
python scripts/build.py yunliao-s3 -c config.local.json \
  --name yunliao-s3 --language zh-CN
```

`-c` resolves the filename under the selected board directory. Keep the local copy untracked and remove it after the device test.

## Flash and observe

```bash
idf.py -p PORT flash
idf.py -p PORT monitor
```

Exit monitor with `Ctrl+]`.

A successful boot should show:

```text
Found 8MB PSRAM device
SKU=yunliao-s3
Es8388AudioCodec initialized
LcdDisplay: Adding LCD display
```

For a Game WSS acceptance, also require a real microphone utterance, BOOT start/stop, TTS playback, room entry through Linx local MCP, `游戏继续`, and `退出游戏`. Logs alone do not prove these passed.

## Known noise

- The activation script may temporarily warn about `LC_CTYPE`; it is non-fatal.
- ESP-SR and private-include CMake warnings are non-fatal in this project/version combination.
- A GPIO 17 conflict has appeared during ES8388 setup while audio still worked. Treat it as fatal only when capture or playback actually fails.

Only claim hardware success for the exact build currently flashed and observed. Do not infer Wi-Fi, microphone, speaker, wake word, Linx MCP, or cloud audio health from a clean boot.
