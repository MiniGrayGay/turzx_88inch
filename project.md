# 项目实现说明

本文档记录 `go/` 目录内独立 Go 驱动的实现约定，后续把 Go 版本单独拿出来时可以直接参考。

## 目标屏幕

只针对这一块屏幕开发：

- 型号：Turing Smart Screen 8.8 寸 Rev C
- 已确认屏幕 ID：`chs_88inch.dev1_rom1.90`
- 已确认 ROM：`90`
- 待机/控制态 USB：`1A86:CA88`
- 待机/控制态序列号：`CT88INCH`
- 开机/数据态 USB：`0525:A4A7`
- 串口协议波特率：`115200`
- 协议原生分辨率：`480x1920`
- 协议原生方向：竖屏
- 已确认像素格式：`BGRA`

驱动层固定按原生竖屏协议开发：

```text
width  = 480
height = 1920
```

横屏布局、UI 旋转、主题坐标转换都应由上层业务处理。驱动服务只接收已经转换好的 `480x1920` 全屏图或原生坐标内的局部矩形。

## 目录结构

```text
go/
  go.mod
  go.sum
  .gitignore
  README.md
  project.md
  cmd/turing88/
    main.go                 CLI 入口，同时默认启动常驻服务
    server.go               常驻 JSON RPC 服务
    default_splash.go       gzip + base64 内嵌的默认开机画面 BGRA
    default_splash_test.go
    local_rpc_windows.go    Windows Named Pipe 入口（build tag: windows）
    local_rpc_unix.go       Linux / 非 Windows Unix socket 入口
    server_test.go
  turing88/
    driver.go               串口协议驱动
    driver_test.go
  tools/
    generate_default_splash/main.go   离线生成 default_splash.go 的工具
  tmp/                       本地测试图片 / 日志（已 gitignore）
  bin/                       交叉编译输出（已 gitignore）
```

`turing88/driver.go` 是串口协议驱动。对外只暴露原生竖屏入口 `DisplayNativeImage` / `DisplayNativeBGRA`，以及保留给旧 CLI 的带旋转入口 `DisplayImage`。

`cmd/turing88/server.go` 是常驻 JSON RPC 服务，负责 HTTP/WebSocket 调试入口和本机 IPC 调用入口。

`cmd/turing88/local_rpc_windows.go` 与 `cmd/turing88/local_rpc_unix.go` 通过 build tag 区分平台：Windows 注册 Named Pipe，其余平台注册 Unix socket。

`cmd/turing88/main.go` 保留旧 CLI 调试入口，同时默认启动服务。

`tools/generate_default_splash/main.go` 是离线工具：把根目录 `4_1.png` 缩放、转码后重新生成 `cmd/turing88/default_splash.go`。常规运行不需要它。

## 构建与交叉编译

驱动只依赖纯 Go 的 `go.bug.st/serial` 和 `gorilla/websocket`，没有 cgo 依赖，所以可以关掉 cgo 做静态交叉编译，编出来的二进制不依赖目标机上的任何动态库。

体积优化约定：

```text
CGO_ENABLED=0            关闭 cgo，静态链接，便于跨机分发
-trimpath                去掉二进制里的绝对路径，减小体积并提升可复现性
-ldflags="-s -w"         去掉符号表（-s）和 DWARF 调试信息（-w）
```

不使用 UPX 压缩。当前两个目标的产物大约：

```text
bin/turing88-windows-amd64.exe   约 10.2 MB
bin/turing88-linux-amd64         约 9.9 MB
```

输出统一放在 `bin/`，该目录已在 `.gitignore` 中忽略，不会被提交。具体命令见 `README.md` 的“构建二进制”一节。

## 常驻服务模式

默认命令：

```powershell
go run .\cmd\turing88
```

等价于：

```powershell
go run .\cmd\turing88 -action serve -port AUTO -brightness 40 -orientation native -pipe ct88inch -unix-socket /tmp/ct88inch.sock -listen 127.0.0.1:60880
```

服务启动后执行：

```text
打开/唤醒串口
-> HELLO
-> 校验 chs_88inch
-> ScreenOn
-> SetBrightness
-> SetOrientation
-> 发送内嵌默认画面
-> 注册 Windows Named Pipe \\.\pipe\ct88inch
-> 注册 Linux / 非 Windows Unix socket unix:///tmp/ct88inch.sock
-> 保持串口连接
-> 等待 Named Pipe / HTTP POST / WebSocket JSON RPC
```

Named Pipe 或 Unix socket 注册失败时不影响主服务启动：如果入口已被其他进程占用，本次启动直接跳过该入口。HTTP 监听端口默认 `60880`，如果端口冲突，会从配置端口开始自动向下递减，直到找到可用端口。

普通 `show` / `update` 请求不会重新打开串口，也不会重新初始化屏幕。

默认初始化画面来自根目录 `4_1.png`。生成器会先把它缩放到物理横屏尺寸 `1920x480`，再转成屏幕协议需要的原生 `480x1920` BGRA。生成后的数据通过 gzip + base64 内嵌在：

```text
go/cmd/turing88/default_splash.go
```

运行时只解包内嵌 BGRA 数据并发送，不再解码 PNG，也不依赖 `4_1.png` 文件存在。

## 自动串口识别

`AUTO` 逻辑：

1. 先查找开机/数据态端口。
2. 如果找到 `0525:A4A7`，直接使用。
3. 如果未找到，则查找待机/控制态端口。
4. 如果发现 `1A86:CA88` 或 `CT88INCH`，尝试打开再关闭来唤醒。
5. 等待开机/数据态端口出现。

目前本机实测：

```text
待机/控制态: COM3, USB 1A86:CA88, serial CT88INCH
开机/数据态: COM4, USB 0525:A4A7
```

`0525:A4A7` 是 Python 原版 Rev C 驱动中已有的开机态识别特征。`1A86:CA88` / `CT88INCH` 来自本机实测。

## JSON RPC

Named Pipe、Unix socket、HTTP POST 和 WebSocket 共用同一个 JSON 请求体：

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

字段规则：

- 字段可以缺失。
- 字段可以传空字符串。
- 空字段不会覆盖当前状态。
- 数字字段既可以传字符串，也可以传数字。
- `action` 为空时等同 `status`。
- `port` 默认 `AUTO`，只在 `init` / `reconnect` 动作中生效。
- `brightness` 默认 `40`。
- `orientation` 默认 `native`，表示驱动层不做旋转。
- 旧旋转枚举里 `native` 对应 `ReversePortrait`；`Portrait` 常量不是默认值，它表示相对原生方向再旋转 180 度。
- `orientation` 也兼容 `none`、`no-rotation`、`reverse-portrait` 等别名。
- `init` / `reconnect` 可以只传 `action`，缺省字段会沿用当前状态或服务启动默认值。
- `brightness` / `orientation` 缺省或与当前状态一致时，不会重新发送设置命令。
- 屏幕已初始化、端口未变化且未传 `reset=true` 时，`init` 不会重新打开串口或重新初始化屏幕。
- `show` / `update` 不依赖 `port`、`brightness`、`orientation`。
- `show` 固定按全屏刷新处理，坐标缺失或为 `0` 都是左上角 `0,0`。
- `update` 是局部刷新，`position_X` / `position_Y` 缺失时默认为 `0,0`。
- `update` 和 HTTP `POST /image` 的图片超出 `480x1920` 可见范围时会自动裁剪，完全不可见时才报错。

Windows 本机 RPC 主入口：

```text
\\.\pipe\ct88inch
```

如果同名 Named Pipe 已经存在或被占用，服务会跳过注册，不会退出。

Pipe 内传输 JSON 对象。客户端写入一个 JSON 对象，服务端返回一个 JSON 对象；同一个连接可以连续发送多条请求。

Linux / 非 Windows 本机 RPC 主入口：

```text
unix:///tmp/ct88inch.sock
```

Unix socket 内也传输 JSON 对象。客户端写入一个 JSON 对象，服务端返回一个 JSON 对象；同一个连接可以连续发送多条请求。

如果 Unix socket 路径已有活跃服务，服务会跳过注册；如果只是陈旧 socket 文件，会清理后重新注册。

HTTP 调试入口：

```text
POST http://127.0.0.1:60880/rpc
```

`60880` 是默认端口。端口冲突时会自动尝试 `60879`、`60878` 等更低端口，实际地址以启动日志为准。

对 `POST` 路由发 `GET` 请求时，服务返回该路由的请求格式作为自说明文档，不会执行任何屏幕动作：`GET /rpc` 返回 JSON RPC 字段、动作列表和说明；`GET /image` 返回支持的 Content-Type、query 坐标和裁剪规则。`GET /` 返回路由索引。

HTTP 图片快捷入口：

```text
POST http://127.0.0.1:60880/image
```

`/image` 不走 JSON RPC 包装，支持直接 POST 图片二进制，也支持 `multipart/form-data`。multipart 中任意字段都可以放文件、`data:image/...;base64,...`、`base64:...` 或纯 base64 图片文本；如果有多个字段，按读取顺序使用第一个能解码成图片的字段。坐标通过 query string 传，支持 `x` / `y` 或 `position_X` / `position_Y`。

`/image` 有两种模式：

- 不带坐标：整屏自适应。按宽高比判断方向，竖图（高 ≥ 宽）保持 `native` 不旋转，横图（宽 > 高）把方向设为 `landscape` 并旋转；长边缩放到 `1920`，短边居中裁剪到填满，按全屏刷新发送。横图走的是驱动 `DisplayImage` 的旋转入口（软件旋转，和默认开机画面的旋转方式一致）。
- 带坐标（`x` / `y`）：原生竖屏坐标局部刷新，不缩放、不旋转，越界自动裁剪。

整屏自适应用的是 `golang.org/x/image/draw` 的 `CatmullRom` 缩放 + 居中裁剪（cover）。对正常横图等价于“长边到 1920、短边取中间”，对极端宽高比则按覆盖整屏处理，保证不留黑边。

WebSocket 调试入口：

```text
ws://127.0.0.1:60880/rpc
```

WebSocket 每条消息都是 JSON 文本，不是 gRPC，也不是 protobuf。

## RPC 动作

```text
status              返回状态
ports / list        列出串口
init / reconnect    重新打开并初始化屏幕
on                  打开屏幕
off                 关闭屏幕，服务继续运行
brightness          设置亮度
clear               清屏
show / full         全屏刷新
update / rect       局部刷新
```

`clear` 可额外传：

```json
{"action":"clear","color":"#000000"}
```

## 图片输入

`image` 字段当前支持：

```text
本机文件路径
data:image/...;base64,...
base64:...
```

推荐优先用本机文件路径，避免 JSON body 过大。

`show` 要求图片尺寸必须是：

```text
480x1920
```

`update` 的图片尺寸可以小于全屏，也可以超出屏幕边界。未越界时完整发送；满足下面条件时不会发生裁剪：

```text
position_X + image_width  <= 480
position_Y + image_height <= 1920
```

现在驱动会裁剪越界区域：图片或矩形超出右侧、下侧、左侧或上侧边界时，只发送仍在屏幕内的部分；矩形与屏幕没有任何交集时才返回错误。

## 坐标约定

所有 RPC 坐标都是原生竖屏左上角坐标：

```text
x: 0..479
y: 0..1919
```

局部刷新地址计算：

```text
address = (y + row) * 480 + x
```

其中：

```text
y   = 矩形左上角 Y
row = 当前矩形内部行号
x   = 矩形左上角 X
```

旧 CLI 的 `DisplayImage` 仍保留方向旋转逻辑。服务端使用的是新增的原生入口：

```text
DisplayNativeImage
DisplayNativeBGRA
```

这两个入口不做横竖屏旋转。

注意命名：旧 `Orientation` 枚举中，`ReversePortrait` 是当前默认的 `native` / 不旋转模式；`Portrait` 不是默认值。

## 主要命令字节

```text
HELLO                  01 ef 69 00 00 00 01 00 00 00 c5 d3
OPTIONS                7d ef 69 00 00 00 05 00 00 00 2d
RESTART                84 ef 69 00 00 00 01
TURNOFF                83 ef 69 00 00 00 01
SET_BRIGHTNESS         7b ef 69 00 00 00 01 00 00 00
STOP_VIDEO             79 ef 69 00 00 00 01
STOP_MEDIA             96 ef 69 00 00 00 01
QUERY_STATUS           cf ef 69 00 00 00 01
PRE_UPDATE_BITMAP      86 ef 69 00 00 00 01
START_DISPLAY_BITMAP   2c
UPDATE_BITMAP          cc ef 69 00
DISPLAY_BITMAP_8INCH   c8 ef 69 00 38 40
```

封包规则：

- 大多数命令补齐到 250 字节整数倍。
- 默认 padding 是 `00`。
- `START_DISPLAY_BITMAP` 使用 `2c` 作为 padding。
- 全屏图按 249 字节分块，块之间插入 `00`。
- 局部刷新行 payload 超过 250 字节时同样按 249 字节分块，块之间插入 `00`。

## 全屏刷新

全屏刷新顺序：

```text
PRE_UPDATE_BITMAP
START_DISPLAY_BITMAP
DISPLAY_BITMAP_8INCH
full BGRA payload
QUERY_STATUS
```

全屏数据量：

```text
480 * 1920 * 4 = 3,686,400 bytes
```

已实测全屏动态刷新：

```text
target_fps = 5
actual_fps = 4.57
avg_send = 219ms
```

结论：持续全屏刷新时 `4 FPS` 比较稳，`5 FPS` 接近当前链路上限。

## 局部刷新

局部刷新顺序：

```text
UPDATE_BITMAP header payload
image row payload
QUERY_STATUS
```

每行数据格式：

```text
3-byte big-endian address
2-byte big-endian row width
row BGRA pixel data
```

局部刷新适合文字、温度、资源占用、进度条等动态区域。

## 像素格式和透明度

实测 ROM 90 只支持：

```text
BGRA
```

已验证不可用：

```text
BGR
RGB565 big-endian
RGB565 little-endian
```

透明度行为：

- MCU 不做 alpha 合成。
- alpha 为 0 的像素不会跳过。
- 透明区域会显示为黑色。

因此文字和图标不要直接发送透明 PNG 图层。正确做法是先在主机端把背景区域裁出来，与文字或图标合成，再发送合成后的矩形。

## 后续开发原则

驱动层只做：

```text
串口识别
屏幕初始化
亮度/开关控制
480x1920 原生坐标图像发送
BGRA 编码
JSON RPC 服务
```

业务层负责：

```text
主题解析
布局
横屏/竖屏转换
字体加载
文字渲染
指标采集
脏矩形调度
动画帧生成
```

这样后续可以把 `go/` 目录单独拿出来，作为一个只负责屏幕通信的轻量驱动服务。
