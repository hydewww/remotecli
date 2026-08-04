# RemoteCLI

[English README](README.md)

RemoteCLI 是一个面向 [OpenCLI] 的跨平台远程进程代理。它不会把 OpenCLI
嵌入或重新实现一遍，而是在服务端启动本机的 `opencli` 子进程，因此能够
继续使用服务端用户的 HOME、`~/.opencli`、Chrome 登录会话、插件、外部 CLI
以及本地 OpenCLI 守护进程。

客户端和服务端使用同一个 Go 二进制文件。只有运行 `remotecli serve` 的
机器需要安装 Node.js 和 OpenCLI；调用端只需要 RemoteCLI 二进制文件。

## 工作方式

```text
调用端 remotecli ── HTTP + Bearer Token ──> 服务端 remotecli serve
                                               │
                                               └─> 本机 opencli 子进程
```

除了 `config` 和 `serve` 之外，RemoteCLI 会把命令行参数原样作为参数数组
发送给服务端的 OpenCLI，不经过 shell 拼接或解释。

## 快速开始

### 1. 在安装了 OpenCLI 的机器上启动服务

建议把 token 放在权限受限的文件中：

```bash
umask 077
mkdir -p ~/.config
printf '%s\n' '替换为一个足够长的随机 token' > ~/.config/remotecli-token

remotecli serve \
  --bind 127.0.0.1 \
  --port 19826 \
  --opencli-bin opencli \
  --token-file ~/.config/remotecli-token
```

如果需要让其他机器访问，把 `--bind` 改为明确的 LAN 或私有网络地址，
并使用防火墙、Tailscale、SSH 隧道或支持 TLS 的反向代理保护服务。服务端
默认只监听 `127.0.0.1`。

也可以通过环境变量提供服务端 token：

```bash
export REMOTECLI_API_TOKEN='替换为 token'
remotecli serve --opencli-bin opencli
```

### 2. 在调用端配置服务

```bash
remotecli config http://opencli-host:19826 --token-file ~/.config/remotecli-token
remotecli config --show
```

随后可以像调用 OpenCLI 一样执行命令：

```bash
remotecli list -f json
remotecli bilibili hot --limit 5 -f json
```

`config --show` 只显示 token 是否已配置，不会输出 token 内容。临时配置
也可以使用环境变量覆盖保存的配置：

```bash
REMOTECLI_ENDPOINT=http://opencli-host:19826 \
REMOTECLI_TOKEN='替换为 token' \
remotecli list -f json
```

## 配置命令

```bash
remotecli config <endpoint> [--token <token> | --token-file <path>]
remotecli config --show
remotecli config --clear
```

服务端常用参数如下：

```text
--bind                  监听地址，默认 127.0.0.1
--port                  监听端口，默认 19826
--opencli-bin           OpenCLI 可执行文件路径或 PATH 中的名称
--token-file            Bearer token 文件
--run-root              请求工作区和 artifact 的保存目录
--concurrency           最大并发 OpenCLI 进程数
--command-timeout       单个命令的最长执行时间
--retention             artifact 保留时间
--max-output            单个 stdout/stderr 的最大捕获字节数
--max-artifact-size     单个 artifact 的最大大小
--max-artifacts-total   单次请求 artifact 的总大小上限
```

服务端 token 可以通过 `--token-file`、`--token` 或
`REMOTECLI_API_TOKEN` 提供，但三者只能选择一个；生产环境优先使用
`--token-file`。

## Artifact 文件

每次远程请求都会在独立的临时工作区中运行。请求工作区中的普通文件会作
为 artifact 返回，调用端会按相对路径下载到当前目录。例如远端命令写入
`./xhs/image.jpg`，调用端会保存到本地的 `./xhs/image.jpg`。

客户端会拒绝覆盖已有文件、绝对路径、路径穿越和包含符号链接父目录的
目标，并会校验服务端返回的文件大小和 SHA-256。请求工作区以外的文件不
会被返回；当前版本也不会上传调用端的输入文件，因此浏览器上传等命令
使用的路径必须存在于 OpenCLI 服务端机器上。

## 安全边界

- Bearer token 拥有与服务端本机 OpenCLI 用户相同的执行权限，包括浏览器
  写操作、插件和已注册的外部 CLI。
- 不要把服务绑定到不受信任的公网；HTTP 服务应放在私有网络或 TLS 反向
  代理后面。
- RemoteCLI 不会暴露 OpenCLI 自己的浏览器守护进程端口，也不会通过 API
  传输 Chrome cookie 或浏览器 profile。
- token 文件应只允许服务账号读取；Unix 系统可执行
  `chmod 600 <token-file>`。

## API

健康检查不需要认证：

```bash
curl http://127.0.0.1:19826/healthz
```

执行和 artifact 下载接口需要 `Authorization: Bearer <token>`：

- `POST /v1/execute`：执行本机 OpenCLI，参数以 JSON 数组传递。
- `GET /v1/runs/<run-id>/artifacts/<artifact-id>`：下载执行结果中的
  artifact。

完整请求/响应示例见 [docs/api.md](docs/api.md)。macOS launchd、Linux
systemd user service 和 Windows Task Scheduler 配置见
[docs/service-guide.md](docs/service-guide.md)。

## 构建与开发

需要 Go 1.22 或更高版本：

```bash
go test ./...
go vet ./...
go build ./cmd/remotecli
```

本地交叉构建示例：

```bash
GOOS=linux GOARCH=amd64 go build -o remotecli-linux-amd64 ./cmd/remotecli
GOOS=darwin GOARCH=arm64 go build -o remotecli-darwin-arm64 ./cmd/remotecli
GOOS=windows GOARCH=amd64 go build -o remotecli-windows-amd64.exe ./cmd/remotecli
```

GitHub Actions 会在 Linux、macOS、Windows runner 上运行测试和静态检查，
并为 Linux、macOS、Windows 的 amd64/arm64 构建二进制 artifact。

[OpenCLI]: https://github.com/jackwener/opencli
