# Self-hosting

Trackdown is a single static binary with no external service dependencies — self-hosting never requires Docker or a container orchestrator. This page covers running it as a persistent service on each major platform.

**Scope note:** on Linux and macOS, these are documented, well-understood mechanisms (`systemd`, `launchd`) driving Trackdown's own graceful shutdown (it already handles `SIGINT`/service-stop signals cleanly via `signal.NotifyContext`). On Windows, Trackdown registers itself directly with the Service Control Manager — no external tool required (see below); NSSM remains documented as an alternative for operators who specifically want its process-supervision behavior (auto-restart on crash), which a bare native service doesn't provide on its own.

## Windows (native service)

Trackdown can register itself as a real Windows service directly — no NSSM or other wrapper needed. `trackdown serve` auto-detects when it's been launched by the Service Control Manager (`svc.IsWindowsService()`) and switches into service mode transparently; the same graceful-shutdown path (context cancellation → `srv.Shutdown` with a bounded timeout) handles both an OS signal in the foreground and an SCM stop/shutdown control request identically.

```powershell
# Run as Administrator. Pass whatever flags you'd give `trackdown serve` directly.
trackdown service install -addr :8080 -db C:\ProgramData\Trackdown\trackdown.db
trackdown service start
```

Set `TRACKDOWN_ADMIN_PASSWORD` as a real system/service environment variable (System Properties → Environment Variables, or `setx /M`) before starting the service — flags aren't the right place for it (visible via Task Manager), and the SCM doesn't read a `.env` file the way systemd's `EnvironmentFile=` does.

```powershell
trackdown service stop
trackdown service uninstall
```

**Alternative — NSSM**: if you want NSSM's own process supervision (e.g. auto-restart if Trackdown itself crashes, which the native service above does not add on its own — the SCM only restarts a service if you've separately configured its recovery actions), [NSSM](https://nssm.cc/) wraps the binary the same way:

```powershell
nssm install Trackdown "C:\Program Files\Trackdown\trackdown.exe" serve -addr :8080 -db C:\ProgramData\Trackdown\trackdown.db
nssm set Trackdown AppEnvironmentExtra TRACKDOWN_ADMIN_PASSWORD=your-strong-password-here
nssm set Trackdown AppStdout C:\ProgramData\Trackdown\trackdown.log
nssm set Trackdown AppStderr C:\ProgramData\Trackdown\trackdown.log
nssm set Trackdown Start SERVICE_AUTO_START
nssm start Trackdown
```

To stop/restart: `nssm stop Trackdown`, `nssm restart Trackdown`. To uninstall: `nssm remove Trackdown confirm`. Don't run both the native service and an NSSM-wrapped instance at once — pick one.

**Alternative — Task Scheduler** (no extra tool download, but weaker service semantics than either of the above): create a task triggered "At startup," action = the `trackdown.exe` path with your arguments, and check "Restart on failure" in the task's Settings tab.

**Testing note:** the native-service control-loop logic (translating SCM stop/shutdown requests into graceful shutdown) is unit-tested (`cmd/trackdown/service_windows_test.go`) without needing an actual SCM, and the install/uninstall/start/stop commands were verified to fail with a clear, actionable error when not run elevated (`Access is denied` → "try running as Administrator"). The full install→start→stop→uninstall lifecycle against a real, running SCM has not been verified in this development environment specifically (it lacks Administrator rights) — if you hit anything unexpected running it for real, please report it.

## Linux (systemd)

```ini
# /etc/systemd/system/trackdown.service
[Unit]
Description=Trackdown error tracking server
After=network.target

[Service]
Type=simple
User=trackdown
WorkingDirectory=/var/lib/trackdown
EnvironmentFile=/etc/trackdown/trackdown.env
ExecStart=/usr/local/bin/trackdown serve -addr :8080 -db /var/lib/trackdown/trackdown.db
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

`/etc/trackdown/trackdown.env` (readable only by the `trackdown` user — `chmod 600`):

```
TRACKDOWN_ADMIN_PASSWORD=your-strong-password-here
```

**Use `EnvironmentFile=`, not an inline `Environment=TRACKDOWN_ADMIN_PASSWORD=...` line in the unit file** — the unit file itself is often world-readable and its contents show up in `systemctl cat`/`systemctl show`, while an `EnvironmentFile` with restrictive permissions doesn't leak the password that way.

```bash
sudo useradd --system --home /var/lib/trackdown --shell /usr/sbin/nologin trackdown
sudo mkdir -p /var/lib/trackdown && sudo chown trackdown:trackdown /var/lib/trackdown
sudo systemctl daemon-reload
sudo systemctl enable --now trackdown
sudo systemctl status trackdown
journalctl -u trackdown -f   # tail logs
```

## macOS (launchd)

launchd's plist format has no direct equivalent of systemd's `EnvironmentFile=`, so route the admin password through a small wrapper script instead of embedding it in the plist (plist files are plain XML and not treated as sensitive by default):

```bash
#!/bin/bash
# /usr/local/etc/trackdown/run.sh -- chmod 700, owned by the service user
set -a
source /usr/local/etc/trackdown/trackdown.env   # chmod 600: TRACKDOWN_ADMIN_PASSWORD=...
set +a
exec /usr/local/bin/trackdown serve -addr :8080 -db /usr/local/var/trackdown/trackdown.db
```

```xml
<!-- ~/Library/LaunchAgents/dev.trackdown.server.plist (per-user) or
     /Library/LaunchDaemons/dev.trackdown.server.plist (system-wide, needs sudo) -->
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>dev.trackdown.server</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/local/etc/trackdown/run.sh</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/usr/local/var/log/trackdown.log</string>
    <key>StandardErrorPath</key>
    <string>/usr/local/var/log/trackdown.error.log</string>
</dict>
</plist>
```

```bash
chmod +x /usr/local/etc/trackdown/run.sh
launchctl load ~/Library/LaunchAgents/dev.trackdown.server.plist
launchctl start dev.trackdown.server
```

## Reverse proxy

Trackdown serves plain HTTP; put it behind a reverse proxy (Caddy, nginx, or your platform's load balancer) for TLS termination. If your proxy sets `X-Forwarded-Proto: https`, the web UI's project setup page correctly reflects `https://` in the DSN it renders.

## Backups

Don't just copy the SQLite file while the server is running — a raw file copy mid-write can produce a torn, inconsistent snapshot. Instead:

```bash
trackdown backup -db trackdown.db /path/to/backups/trackdown-$(date +%Y%m%d).db
```

This uses SQLite's `VACUUM INTO` to produce a complete, consistent, independently-openable snapshot in one step — safe to run while the server is live, and safe to schedule via cron / Windows Task Scheduler alongside your retention job. The fallback (stop the server, copy the file) still works if you'd rather not use the subcommand, but `trackdown backup` is the recommended, safer path.

## Retention

Old events (not issues — those stay forever as historical summaries) can be pruned automatically. Either pass `-retention-days N` to `trackdown serve` for an in-process daily cleanup, or run `trackdown gc -db trackdown.db -retention-days N` from cron/Task Scheduler on your own schedule. See [Configuration](configuration.md) for details. Retention is off by default — Trackdown never deletes data unless you opt in.

## Upgrades

Until a stable release process exists, upgrading means replacing the binary and restarting the service. Schema migrations (when introduced) will be applied automatically on startup. Take a `trackdown backup` before upgrading across any release that might change the schema.
