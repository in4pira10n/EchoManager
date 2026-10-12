# EchoManager

EchoManager is a lightweight, local-first process manager for registering and operating long-running applications on the host machine. It is written in Go and serves a browser-based interface built with vanilla JavaScript and EchoKit.

## Repository Layout

- `main.go` — small executable entry point.
- `internal/server/` — application server, authentication, device management, process control, and Go tests.
- `web/` — HTML, CSS, JavaScript, static assets, and `embed.go` for embedding the UI.
- `rsrc_windows_amd64.syso` — Windows amd64 executable icon resource.
- `assets/icons/` — canonical application icon source.
- `scripts/` — WinRM setup and icon-generation helpers.
- `config.example.json`, `go.mod`, and `go.sum` — sample runtime configuration and Go module metadata.

## Features

- Register, edit, start, stop, and remove application instances.
- Track process state, PID, configured service port, and startup time.
- Capture each managed process's standard output and standard error in per-instance logs.
- Store cropped instance icons locally.
- Refresh instance state on demand; startup readiness is checked while a newly started process is settling.
- Access the manager from the host or another device on the local network with an administrator account.
- Register remote macOS / Linux devices over SSH and Windows devices over HTTPS WinRM, then manage their instances from the central Manager.
- Use the system, light, or dark appearance and a configurable accent color.

EchoManager starts the configured executable directly with argument and environment arrays on the selected target device. Local processes use Go's parameterized process APIs. Remote Unix processes are launched through a generated, quoted Bash script over SSH; remote Windows processes are launched through a generated PowerShell script over WinRM. Readiness is determined by whether the configured TCP port accepts connections; the manager does not call an application-specific HTTP health endpoint.

## Requirements

- Go 1.24 or later.
- Python 3 and Pillow are only needed to regenerate icon assets; see `scripts/requirements-icons.txt`.
- A separate EchoKit checkout. EchoKit is served by EchoManager at `/kit/` and is not vendored in this repository.
- The EchoManager web interface and its own static assets are embedded in the built executable.
- Remote Unix-like targets need an SSH server and a `bash` shell. The Manager host must have the target host key in its own `~/.ssh/known_hosts`; host-key checking is never silently disabled.
- Remote Windows targets need PowerShell remoting over HTTPS (WinRM, normally port `5986`). The target certificate is verified by default; skipping verification is an explicit per-device setting for trusted LAN deployments only.

Set `ECHOKIT_DIR` to select an EchoKit checkout. Otherwise, a built executable searches for an `EchoKit` directory beside itself or beside its containing directory. When running from source, it also checks for `EchoKit` beside the current project directory. The required EchoKit component modules, styles, design tokens, and SF Symbols must be available at runtime. The server exposes only these runtime assets, not the rest of either source checkout.

On first launch, EchoManager opens an initialization page that follows the app's authentication-page layout. Enter the EchoKit root (the folder containing `css`, `components`, `tokens`, and `SFSymbols`) and create the first administrator account. After initialization, sign in with that account. Registration of additional administrator accounts requires authorization with an existing administrator's credentials. All registered accounts are administrators; visitor roles are not used. If the configured Kit path later becomes invalid, the initialization page requires an administrator login before saving a replacement path.

Runtime options can also be placed in an optional `config.json` beside the executable. Copy `config.example.json` as a starting point. If no executable-side file exists, EchoManager checks the current working directory, which is useful when running from source. Relative paths in the file are resolved from the file's directory. Environment variables override file values: `ECHOKIT_DIR`, `PORT`, and `ECHOMANAGER_DATA_DIR`. If `ECHOKIT_DIR` is set but invalid, it must be corrected in the environment because environment values intentionally take precedence over the setup page's saved config. The settings page's copy, icons, options, and browser preference defaults are centralized in `web/settings-config.js` and embedded at build time; rebuild after changing them.

## Run from Source

Run the command from the EchoManager project directory to build and run the Go module. The web interface is embedded in the executable at compile time.

On macOS or Linux, with EchoKit at `../EchoKit`:

```sh
go run .
```

To override the configuration file from the environment:

```sh
ECHOKIT_DIR="/path/to/EchoKit" PORT=3000 go run .
```

On Windows PowerShell:

```powershell
$env:ECHOKIT_DIR = "C:\path\to\EchoKit"
$env:PORT = "3000"
go run .
```

Open `http://127.0.0.1:3000`. On first run, complete initialization and create the first administrator account. The manager defaults to port `3000`; set `PORT` to change it. At startup, the console prints the local URL, a LAN URL when available, the data directory, and the configuration file path.

## Build

Build for the current platform from the project directory:

```sh
mkdir -p build
go build -o build/EchoManager .
```

Cross-compile a Windows x86-64 executable from macOS or Linux:

```sh
mkdir -p dist
GOOS=windows GOARCH=amd64 go build -o dist/EchoManager-windows-amd64.exe .
```

The built executable can be launched from any working directory; its EchoManager web interface and its own static assets are embedded. EchoKit is not embedded and must remain available through the default lookup locations or `ECHOKIT_DIR`.

## Application Icons

The canonical macOS icon is `assets/icons/AppIcon.icns`. The browser favicon (`web/static/assets/favicon.ico`), PNG page/app marks, and the Windows amd64 executable resource are generated from this same icon. The root-level `rsrc_windows_amd64.syso` is automatically linked by Go when building for Windows amd64, so the executable carries the application icon as well as the browser having its favicon. `AppIcon.icns` is ready to place in a macOS app bundle's `Contents/Resources` directory under that name.

To regenerate the derived icon assets on macOS, install the optional Python dependency with `python3 -m pip install -r scripts/requirements-icons.txt`, then run `sh scripts/generate-icons.sh`. This requires Apple's `sips`, Go, Python 3, and Pillow. Windows cross-builds use the committed `.syso` resource and do not need `rsrc` at build time.

## Devices and Remote Instances

EchoManager is a central manager: it runs on one host, while other computers are registered as target devices. All logged-in administrators see the same device and instance records. The Manager does not discover the LAN automatically and no target-side EchoManager Agent is required.

When adding a device in Settings:

- Choose `macOS / Linux` with `SSH`, or `Windows` with `HTTPS WinRM`.
- For SSH password authentication, enter the target account password. For SSH key authentication, enter an absolute private-key path on the Manager host.
- For WinRM, use the target Windows account and HTTPS port `5986`. The optional certificate bypass is intended only for a trusted LAN.
- Use `Test connection` after saving to update the device's online state. A connection test only probes the remote shell; it does not run a managed instance.

### Prepare a Windows Target for WinRM

The repository includes a Windows setup helper in `scripts/`. Copy both `configure-winrm-https.bat` and `configure-winrm-https.ps1` to the Windows target and keep them in the same folder. Double-click the `.bat` file; the PowerShell script requests Administrator elevation automatically. It asks for the central EchoManager host's IPv4 address, starts WinRM, configures an HTTPS listener on port `5986`, enables Basic authentication only for the HTTPS transport, and adds a Windows Firewall allow rule scoped to that Manager IP. It removes a newly-created HTTP listener and newly-enabled HTTP firewall rules; pre-existing HTTP configuration is left unchanged.

Before running it, connect the target to a trusted LAN and set that network profile to `Private` (or use an authenticated `Domain` profile). Find the Manager host's IPv4 address with `ipconfig` on the Manager machine; enter that address when prompted. Then add the Windows target in EchoManager using its LAN address, `Windows`, `HTTPS WinRM`, port `5986`, and a Windows account permitted to use PowerShell remoting and run the managed processes. For a local Windows account, use `COMPUTERNAME\username` or `.\username` if the plain username is not accepted.

The helper creates a self-signed certificate if the target does not already have an HTTPS WinRM listener. Because that certificate usually identifies the computer by DNS name rather than by its LAN IP, enable EchoManager's certificate-verification bypass for this device only when the connection stays on a trusted LAN. For normal certificate verification, install a trusted certificate whose identity matches the address EchoManager uses. The helper does not remove or rewrite pre-existing HTTP listeners or firewall rules; a pre-existing broad allow rule for TCP `5986` could permit more than the Manager host, so review existing inbound rules if this PC was previously configured for WinRM HTTPS.

An instance's executable path, arguments, environment, and configured service port are interpreted on its selected device. The `Go to page` action uses the target device address for remote instances and the current browser host for local instances. The Manager does not stop a process merely because its port matches: remote stop additionally verifies the recorded PID, executable path, and process start token/time.

## Data and Configuration

Unless `data_dir` in `config.json` or `ECHOMANAGER_DATA_DIR` is set, persistent data is stored in Go's per-user configuration directory under `EchoManager`. The directory contains:

- `instances.json` — registered instance configuration and the latest saved state.
- `instances.json` also contains registered device connection metadata. Device passwords are encrypted and are not written in plaintext.
- `device-secrets.key` — private AES-GCM key used to decrypt saved device passwords. Keep it with `instances.json`; losing it requires re-entering device credentials.
- `users.json` — administrator usernames and bcrypt password hashes.
- `sessions.json` — active browser sessions and their seven-day expiration times; logging out removes the session.
- `logs/` — per-instance process output and error logs.
- `icons/` — uploaded instance icons.

Set `data_dir` in `config.json` or `ECHOMANAGER_DATA_DIR` to choose a different location. This data is outside the source tree by default and should not be committed. Existing `control.token` files are no longer read or used. Authentication is provided by the administrator login; there is no separate LAN control token.

## Security Notes

The HTTP server listens on all network interfaces so that LAN access is possible. Both local and LAN users must sign in with an administrator account before viewing or controlling instances. Passwords are stored as bcrypt hashes; the browser session is held in an HttpOnly, SameSite cookie, expires after seven days, and remains valid across EchoManager restarts until it expires or the user logs out. Session records are stored in the protected manager data directory; protect that directory as you would other authentication secrets.

The built-in server uses plain HTTP. Do not expose it directly to the public internet or an untrusted network. Use EchoManager only on a trusted network with appropriate host firewall rules, and protect the manager data directory.

When stopping a process, EchoManager targets only the process it started (and its process tree where supported). It does not kill unrelated applications based only on a port number. On Unix-like systems it first requests graceful termination and then escalates if needed; Windows uses the operating system's `taskkill` utility.

Closing EchoManager stops its web service but leaves managed processes running. Start EchoManager again to resume management; processes still listening after restart are reported for manual verification rather than being adopted automatically.

## Development Checks

```sh
go test ./...
go vet ./...
go build ./...
```

Cross-compile without attempting to run the foreign executable:

```sh
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go build ./...
```

## Project Status

EchoManager is under active development. There is currently no `LICENSE` file; until a license is added, no permission to reuse or redistribute this source should be assumed.
