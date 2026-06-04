# Turing 8.8 寸 Go 驱动

这个目录是针对 Turing Smart Screen 8.8 寸 Rev C 屏幕的独立 Go 驱动。当前主入口是常驻驱动服务：启动时自动识别串口、初始化屏幕，并持续持有 COM 连接；其他程序通过 Named Pipe、Unix socket、HTTP POST 或 WebSocket 发送 JSON RPC 请求。

## 启动

在 `go/` 目录执行：

```powershell
go run .\cmd\turing88
```

等价于：

```powershell
go run .\cmd\turing88 -action serve -port AUTO -brightness 40 -orientation native -pipe ct88inch -unix-socket /tmp/ct88inch.sock -listen 127.0.0.1:60880
```

启动流程：

1. 自动查找屏幕串口。
2. 如果发现待机/控制态设备 `1A86:CA88` 或序列号 `CT88INCH`，先尝试唤醒。
3. 等待开机/数据态设备 `0525:A4A7` 出现。
4. 打开串口并发送 `HELLO`。
5. 确认返回 `chs_88inch...`。
6. 打开屏幕、设置亮度。
7. 发送内嵌默认画面。
8. 保持串口连接。

默认画面来自根目录 `4_1.png`：生成阶段已缩放到 `1920x480`，再转换成协议原生 `480x1920` BGRA，并以 gzip + base64 形式内嵌到 `cmd/turing88/default_splash.go`。运行时不会再读取 `4_1.png`。

默认监听地址是：

```text
\\.\pipe\ct88inch
unix:///tmp/ct88inch.sock
http://127.0.0.1:60880
```

如果 Named Pipe 或 Unix socket 已经被占用，服务会跳过这个本机 RPC 入口并继续运行。HTTP 端口如果被占用，会从配置端口开始自动向下递减，直到找到可用端口，例如 `60880` 被占用时尝试 `60879`。

## RPC 格式

Named Pipe、Unix socket、HTTP POST 和 WebSocket 使用同一份 JSON 结构，字段参考根目录的 `sample.json`：

```json
{
  "action": "",
  "port": "",
  "brightness": "",
  "orientation": "",
  "image": "",
  "position_X": "",
  "position_Y": ""
}
```

下面这些规则对所有传输方式一致：

```text
Windows Named Pipe: \\.\pipe\ct88inch
Unix socket:        unix:///tmp/ct88inch.sock
HTTP POST:          http://127.0.0.1:60880/rpc
WebSocket:          ws://127.0.0.1:60880/rpc
```

字段可以缺失，也可以传空字符串。缺失和空字符串都表示“不覆盖当前状态”，除非该动作本身要求这个字段。

| Key | 可缺省/空值 | 缺省行为 | 支持值 | 生效动作 |
| --- | --- | --- | --- | --- |
| `action` | 可以 | 等同 `status` | `status`, `ports`, `list`, `init`, `initialize`, `reinit`, `reconnect`, `on`, `screen_on`, `off`, `screen_off`, `brightness`, `set_brightness`, `clear`, `show`, `full`, `update`, `rect` | 所有请求 |
| `port` | 可以 | 使用当前端口；服务刚启动时默认 `AUTO` | `AUTO`，或明确串口名，例如 `COM4`、`/dev/ttyACM0` | 只在 `init` / `reconnect` 生效 |
| `brightness` | 可以 | 使用当前亮度；服务刚启动时默认 `40` | `0..100` 的整数；可以传数字或字符串 | `init` / `reconnect` / `brightness`，也可随 `show` / `update` 一起传 |
| `orientation` | 可以 | 使用当前方向；服务刚启动时默认 `native` | `native`, `none`, `no-rotation`, `no_rotation`, `norotation`, `0`, `portrait`, `reverse-portrait`, `reverse_portrait`, `landscape`, `reverse-landscape`, `reverse_landscape` | `init` / `reconnect`，也可随 `show` / `update` 一起传 |
| `image` | 语法上可以；`show` / `update` 必填 | 非图片动作忽略；`show` / `update` 缺失会报错 | 本机文件路径，`data:image/...;base64,...`，`base64:...` | `show` / `full` / `update` / `rect` |
| `position_X` | 可以 | `0` | 整数；可以传数字或字符串；矩形越界时裁剪 | `update` / `rect` |
| `position_Y` | 可以 | `0` | 整数；可以传数字或字符串；矩形越界时裁剪 | `update` / `rect` |

额外支持字段：

| Key | 可缺省/空值 | 缺省行为 | 支持值 | 生效动作 |
| --- | --- | --- | --- | --- |
| `color` | 可以 | `#000000` | `#RRGGBB` | `clear` |
| `reset` | 可以 | `false` | `true`, `false`, `1`, `0`, `yes`, `no`, `on`, `off`；可以传布尔值或字符串 | `init` / `reconnect` |

`show` 固定是全屏刷新，图片尺寸必须是 `480x1920`，坐标字段即使传了也按 `0,0` 处理。`update` 是局部刷新，坐标是矩形左上角；图片超出 `480x1920` 的部分会在发送前裁剪掉，完全不可见时才会报错。

`brightness` 和 `orientation` 缺省或与当前状态一致时，不会重新向屏幕发送设置命令。`init` 也是幂等的：屏幕已经初始化、端口没有变化且没有传 `reset=true` 时，不会重新打开串口或重新初始化屏幕。

注意代码命名：协议默认是原生竖屏 `480x1920`；旧旋转枚举里 `native` 对应 `ReversePortrait`，不是 `Portrait` 常量。`Portrait` 表示相对原生方向再旋转 180 度。

## Named Pipe

Windows 本机 RPC 默认使用 Named Pipe：

```text
\\.\pipe\ct88inch
```

如果同名 Named Pipe 已被其他进程占用，驱动服务不会抢占，也不会退出；该入口本次启动不注册。

Pipe 内传输 JSON 对象。客户端写入一段 JSON，服务端返回一段 JSON；同一个连接可以连续发送多条 JSON 请求。

PowerShell 示例：

```powershell
$pipe = [System.IO.Pipes.NamedPipeClientStream]::new(".", "ct88inch", [System.IO.Pipes.PipeDirection]::InOut)
$pipe.Connect(3000)
$writer = [System.IO.StreamWriter]::new($pipe)
$reader = [System.IO.StreamReader]::new($pipe)
$writer.AutoFlush = $true
$writer.WriteLine('{"action":"status"}')
$reader.ReadLine()
$pipe.Dispose()
```

Linux / 非 Windows 本机 RPC 默认使用 Unix socket：

```text
unix:///tmp/ct88inch.sock
```

Unix socket 内同样传输 JSON 对象，一次写入一个 JSON 请求，返回一个 JSON 响应。

如果 socket 路径已有活跃服务占用，驱动服务不会覆盖它；该入口本次启动不注册。如果路径是陈旧 socket 文件，服务会清理后重新注册。

## HTTP POST

HTTP POST 保留为调试入口。

默认 HTTP 端口是 `60880`。如果该端口已被占用，服务会自动尝试 `60879`、`60878`，直到找到可用端口；启动日志会输出实际监听地址。

对 `POST` 路由发 `GET` 请求会返回该路由的请求格式（自说明，不执行任何屏幕动作）：

```powershell
Invoke-RestMethod http://127.0.0.1:60880/rpc      # 返回 JSON RPC 字段、动作列表、说明
Invoke-RestMethod http://127.0.0.1:60880/image    # 返回支持的 Content-Type、query 坐标、裁剪规则
Invoke-RestMethod http://127.0.0.1:60880/          # 返回路由索引
```

状态查询：

```powershell
Invoke-RestMethod -Method POST `
  -Uri http://127.0.0.1:60880/rpc `
  -ContentType "application/json" `
  -Body '{"action":"status"}'
```

全屏刷新。图片必须是原生竖屏 `480x1920`：

```powershell
Invoke-RestMethod -Method POST `
  -Uri http://127.0.0.1:60880/rpc `
  -ContentType "application/json" `
  -Body '{"action":"show","image":".\\tmp\\bgra-fullscreen-source.png"}'
```

局部刷新。坐标是左上角坐标：

```powershell
Invoke-RestMethod -Method POST `
  -Uri http://127.0.0.1:60880/rpc `
  -ContentType "application/json" `
  -Body '{"action":"update","image":".\\tmp\\small.png","position_X":120,"position_Y":40}'
```

HTTP 也提供一个只用于图片上传的快捷入口 `POST /image`，不需要 JSON RPC 包装。它有两种模式：

- **不带坐标（整屏自适应）**：按宽高比判断方向，任意尺寸图片都会被填满整屏。竖图（高 ≥ 宽）保持 `native` 方向不旋转；横图（宽 > 高）自动把方向设为 `landscape` 并旋转。缩放规则是长边缩放到 `1920`，短边居中裁剪到填满，多余部分裁掉。
- **带坐标（局部刷新）**：传了 `x` / `y`（或 `position_X` / `position_Y`）时按原生竖屏坐标做局部刷新，不缩放、不旋转；图片超出 `480x1920` 的部分自动裁剪。

整屏自适应，直接 POST 一张照片（横图自动转 landscape）：

```powershell
Invoke-RestMethod -Method POST `
  -Uri http://127.0.0.1:60880/image `
  -ContentType "image/png" `
  -InFile .\tmp\photo.png
```

局部刷新，直接 POST 图片二进制并带坐标：

```powershell
Invoke-RestMethod -Method POST `
  -Uri "http://127.0.0.1:60880/image?x=120&y=40" `
  -ContentType "image/png" `
  -InFile .\tmp\small.png
```

multipart/form-data 上传文件：

```powershell
curl.exe -X POST "http://127.0.0.1:60880/image?x=120&y=40" -F "file=@.\tmp\small.png"
```

multipart/form-data 中任意字段也可以传 `data:image/...;base64,...`、`base64:...` 或纯 base64 图片文本；如果传了多个字段，服务端按 multipart 读取顺序使用第一个能解码成图片的字段。

重新初始化。只传 `action` 时会继续使用当前端口、亮度和方向；如果服务刚启动，则使用 `AUTO`、亮度 `40`、`native`：

```powershell
Invoke-RestMethod -Method POST `
  -Uri http://127.0.0.1:60880/rpc `
  -ContentType "application/json" `
  -Body '{"action":"init"}'
```

## WebSocket

WebSocket 地址：

```text
ws://127.0.0.1:60880/rpc
```

每条消息发送一段 JSON 文本，格式和 `POST /rpc` 相同。服务端每次返回一段 JSON 响应。

示例消息：

```json
{"action":"show","image":".\\tmp\\bgra-fullscreen-source.png"}
```

## 支持动作

- `status`：返回驱动状态。
- `ports` / `list`：列出串口。
- `init` / `reconnect`：重新打开并初始化屏幕。
- `on`：打开屏幕。
- `off`：关闭屏幕但服务继续运行。
- `brightness`：设置亮度。
- `clear`：清屏，默认黑色；可传 `color:"#RRGGBB"`。
- `show` / `full`：全屏刷新，要求图片尺寸为 `480x1920`。
- `update` / `rect`：局部刷新，坐标为 `position_X`,`position_Y`，越界部分自动裁剪。

## 坐标和像素格式

驱动服务只按屏幕协议的原生竖屏坐标工作：

```text
width  = 480
height = 1920
```

上层如果要做横屏 UI，需要自己先把画面旋转或映射成 `480x1920`，再交给驱动发送。

实测这块 ROM 90 屏幕只支持 BGRA：

```text
B, G, R, A
```

`BGR` 和 `RGB565` 都已验证不可用。透明像素也不会由 MCU 做 alpha 合成，透明区域会变成黑色；文字和图标需要先在主机端与背景合成好，再发送最终图像。

## 调试 CLI

旧的一次性 CLI 入口仍保留用于调试。`-action` 默认是 `serve`（启动常驻服务），其余动作每次都会单独打开并初始化串口：

```powershell
go run .\cmd\turing88 -action list
go run .\cmd\turing88 -action init   -port AUTO
go run .\cmd\turing88 -action show   -port AUTO -image .\tmp\bgra-fullscreen-source.png
go run .\cmd\turing88 -action update -image .\tmp\small.png -x 120 -y 40
go run .\cmd\turing88 -action clear  -color "#000000"
go run .\cmd\turing88 -action off
go run .\cmd\turing88 -action fps    -fps 5 -seconds 20
```

可用动作：

| `-action` | 说明 |
| --- | --- |
| `serve` | 启动常驻服务（默认） |
| `list` | 列出串口 |
| `init` | 打开并初始化屏幕后退出 |
| `show` | 全屏刷新，需要 `-image`（走旧 `DisplayImage` 带旋转入口） |
| `update` | 局部刷新，需要 `-image`，配合 `-x` / `-y` |
| `clear` | 清屏，配合 `-color` |
| `off` | 关闭屏幕 |
| `fps` | 全屏动态刷新压测，配合 `-fps` / `-seconds` |

常用命令行参数：

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `-port` | `AUTO` | 串口名或 `AUTO` |
| `-image` | 空 | `show` / `update` 的图片路径 |
| `-x` / `-y` | `0` | `update` 矩形左上角坐标 |
| `-brightness` | `40` | 亮度 `0..100` |
| `-orientation` | `native` | 方向；CLI 走带旋转入口，`native` 表示不旋转 |
| `-color` | `#000000` | `clear` 的颜色 |
| `-reset` | `false` | 初始化前先 `RESTART` 重启屏幕 |
| `-fps` | `5` | `fps` 压测目标帧率 |
| `-seconds` | `20` | `fps` 压测时长，`0` 表示一直运行 |
| `-listen` / `-pipe` / `-unix-socket` | 见上文 | 仅 `serve` 使用 |

常规使用建议走常驻服务，避免每次刷新都重新打开、初始化串口。CLI 的 `show` / `update` / `clear` 走的是带旋转的旧入口；常驻服务走的是原生 `480x1920` 入口，两者在 `native` 方向下结果一致。

## 构建二进制

驱动没有 cgo 依赖，可以关掉 cgo 做静态交叉编译，产物不依赖目标机的动态库。统一输出到 `bin/`（已 gitignore）。

体积优化参数：`-trimpath` 去掉绝对路径，`-ldflags="-s -w"` 去掉符号表与调试信息。不使用 UPX。

PowerShell（Windows 本机交叉编译两个目标）：

```powershell
$env:CGO_ENABLED = "0"
$env:GOARCH = "amd64"

$env:GOOS = "windows"
go build -trimpath -ldflags="-s -w" -o bin/turing88-windows-amd64.exe ./cmd/turing88

$env:GOOS = "linux"
go build -trimpath -ldflags="-s -w" -o bin/turing88-linux-amd64 ./cmd/turing88

Remove-Item Env:\GOOS, Env:\GOARCH, Env:\CGO_ENABLED
```

bash / Linux / macOS：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o bin/turing88-windows-amd64.exe ./cmd/turing88
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o bin/turing88-linux-amd64 ./cmd/turing88
```

产物大约 10 MB。运行方式和 `go run .\cmd\turing88` 完全一致，例如：

```powershell
.\bin\turing88-windows-amd64.exe -action list
.\bin\turing88-windows-amd64.exe            # 默认启动常驻服务
```

```bash
./bin/turing88-linux-amd64 -action list
./bin/turing88-linux-amd64                  # 默认启动常驻服务，本机 RPC 入口为 /tmp/ct88inch.sock
```

## 验证

```powershell
go test ./...
```
