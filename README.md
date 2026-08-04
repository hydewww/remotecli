# RemoteCLI

RemoteCLI is a small cross-platform process proxy for [OpenCLI]. It does not
embed or reimplement OpenCLI. The server starts the local `opencli` executable
as a child process, so the child keeps the server user's HOME, `~/.opencli`,
Chrome login session, plugins, external CLIs, and local OpenCLI daemon.

The client and server are both built as a single Go binary. Only the machine
running `remotecli serve` needs Node.js and OpenCLI installed.

## Quick start

On the computer that already has OpenCLI and its Chrome login session:

```bash
export REMOTECLI_API_TOKEN='replace-with-a-long-random-token'
remotecli serve --bind 0.0.0.0 --port 19826 --opencli-bin opencli
```

On another computer:

```bash
remotecli config http://opencli-host:19826 --token "$REMOTECLI_API_TOKEN"
remotecli list -f json
remotecli bilibili hot --limit 5 -f json
```

Use `--token-file` instead of putting the token in shell history when
possible. The client sends every non-local command argument unchanged to the
server. `remotecli config` and `remotecli serve` are the only local commands.

## Security

The bearer token grants the same authority as the local OpenCLI user. It can
run browser writes, plugins, registered external CLIs, and any other command
available to that OpenCLI installation. Keep it secret and restrict network
reachability with a firewall, Tailscale, SSH tunnel, or a reverse proxy.

The service defaults to `127.0.0.1`. Binding to `0.0.0.0` is explicit. The
built-in server is HTTP; use a TLS-capable reverse proxy or a private overlay
network when traffic leaves a trusted host network.

The existing OpenCLI browser daemon remains local-only on its own port. RemoteCLI
never exposes that daemon and never sends Chrome cookies or profile data over
the API.

## Configuration

```bash
remotecli config <endpoint> [--token <token> | --token-file <path>]
remotecli config --show
remotecli config --clear
```

`REMOTECLI_ENDPOINT` and `REMOTECLI_TOKEN` override the saved client config.
The server token can be supplied using `--token-file`, `--token`, or
`REMOTECLI_API_TOKEN`.

## Artifacts

Each remote request runs in an isolated temporary workspace. Regular files
created there are returned as artifacts. Relative paths are recreated under
the client's current directory, so a remote `--output ./xhs` can be downloaded
to local `./xhs`. Existing files, absolute paths, path traversal, and symlink
parents are rejected by the client.

Files written outside the request workspace are not returned. Remote input
files for commands such as browser upload are not transferred in this version;
the path must exist on the OpenCLI host.

For direct HTTP integration, see [docs/api.md](docs/api.md).

## Running in the background

The binary intentionally stays a foreground process. Let the platform's
supervisor manage restart and logs. See [docs/service-guide.md](docs/service-guide.md)
for macOS launchd, Ubuntu/Debian systemd user services, and Windows Task
Scheduler examples.

## Development

```bash
go test ./...
go vet ./...
go build ./cmd/remotecli
GOOS=darwin go build ./cmd/remotecli
GOOS=linux go build ./cmd/remotecli
GOOS=windows go build ./cmd/remotecli
```

[OpenCLI]: https://github.com/jackwener/opencli
