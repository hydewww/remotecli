# RemoteCLI background service guide

RemoteCLI itself stays in the foreground so the same binary works on macOS,
Ubuntu, Debian, and Windows. Use the platform supervisor to start it at login,
restart it, and collect logs.

Replace these placeholders before running commands:

- `/path/to/remotecli`: absolute path to the binary
- `/path/to/opencli`: absolute path to the local OpenCLI launcher
- `/path/to/token-file`: owner-readable token file
- `HOST_BIND`: `127.0.0.1`, a LAN address, or a private overlay address

Keep the token file readable only by the service account. Prefer a private
network (for example Tailscale) or a TLS reverse proxy when the endpoint is
not on a trusted LAN.

On Unix systems, verify it with:

```bash
chmod 600 /path/to/token-file
```

## macOS launchd user agent

Create `~/Library/LaunchAgents/com.remotecli.agent.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.remotecli.agent</string>
  <key>ProgramArguments</key>
  <array>
    <string>/path/to/remotecli</string>
    <string>serve</string>
    <string>--bind</string><string>HOST_BIND</string>
    <string>--port</string><string>19826</string>
    <string>--opencli-bin</string><string>/path/to/opencli</string>
    <string>--token-file</string><string>/path/to/token-file</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/tmp/remotecli.out.log</string>
  <key>StandardErrorPath</key><string>/tmp/remotecli.err.log</string>
</dict>
</plist>
```

Load and inspect it:

```bash
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.remotecli.agent.plist
launchctl kickstart -k "gui/$(id -u)/com.remotecli.agent"
launchctl print "gui/$(id -u)/com.remotecli.agent"
```

## Ubuntu/Debian systemd user service

Create `~/.config/systemd/user/remotecli.service`:

```ini
[Unit]
Description=RemoteCLI OpenCLI process proxy
After=network-online.target

[Service]
ExecStart=/path/to/remotecli serve --bind HOST_BIND --port 19826 --opencli-bin /path/to/opencli --token-file /path/to/token-file
Restart=on-failure
RestartSec=3
WorkingDirectory=%h

[Install]
WantedBy=default.target
```

Enable it:

```bash
systemctl --user daemon-reload
systemctl --user enable --now remotecli.service
systemctl --user status remotecli.service
journalctl --user -u remotecli.service -f
```

For a service that must survive logout, enable user lingering:

```bash
loginctl enable-linger "$USER"
```

## Windows Task Scheduler

Run PowerShell as the target user and create a logon task. Use an absolute
binary and token-file path; do not depend on an interactive PATH.

```powershell
schtasks /Create /F /SC ONLOGON /RL LIMITED /TN RemoteCLI /TR '"C:\path\to\remotecli.exe" serve --bind HOST_BIND --port 19826 --opencli-bin "C:\path\to\opencli.exe" --token-file "C:\path\to\token-file"'
schtasks /Run /TN RemoteCLI
schtasks /Query /TN RemoteCLI /V /FO LIST
```

If the npm installation only exposes `opencli.cmd`, pass that path as
`--opencli-bin`; RemoteCLI uses its Windows launcher adapter. For a real
Windows Service with recovery policies, use NSSM or WinSW and point it at the
same `remotecli.exe serve ...` command.

## Health and firewall checks

```bash
curl http://127.0.0.1:19826/healthz
```

The health endpoint is intentionally unauthenticated and contains only local
service metadata. The execution and artifact endpoints always require the
Bearer token.
