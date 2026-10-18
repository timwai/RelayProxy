# macOS system HTTP/HTTPS/SOCKS proxy (without Developer ID)

This opt-in mode is independent of `network.mode=divert` and does not install a Network Extension, a TUN device, or a privileged background service.

## Configuration

```yaml
system_proxy:
  mode: system
  auto_follow: true
  http: true
  socks5: true
proxy:
  http:
    enabled: true
    listen: 127.0.0.1
    port: 8080
  socks5:
    enabled: true
    listen: 127.0.0.1
    port: 1080
```

The default mode is **off**, preserving existing installations. Omitted booleans default to true. `system_proxy.mode` must be `system` or empty. Use the Agent's **本机代理 → macOS 系统代理** controls to change the mode and flags.

Only applications honoring macOS system proxy settings are captured. Native UDP/QUIC, custom resolver traffic, arbitrary direct sockets, and precise per-process routing are **not** covered. The local HTTP/SOCKS listeners continue using the existing RelayProxy DIRECT / PROXY / REJECT routing dialer.

## Activation and privileges

The manager identifies the active network service using `route -n get default` and `networksetup -listnetworkserviceorder`. On the active service, it configures HTTP, Secure Web Proxy, and SOCKS to the Agent's loopback listeners. It doesn't alter PAC, automatic discovery, proxy exclusions, or DNS settings. Active authenticated proxies and active PAC/auto-discovery settings are treated as configuration conflicts and are not overridden.

When `networksetup` reports insufficient privileges, the GUI may trigger an administrator prompt via `osascript`. The helper runs only fixed `/usr/sbin/networksetup` commands with shell-escaped arguments. No root process or persistent privileged helper is installed.

## Persistence, recovery and conflicts

Before changing each proxy type, the manager writes a mode-0600 JSON journal alongside the Agent YAML file (`macos-system-proxy-recovery.json`). It records the network service, the previous effective enabled/server/port state, and the Agent's expected state.

- On a normal shutdown or when switching off system proxy mode, RelayProxy restores prior settings.
- With `auto_follow: true`, it polls the default network service every seven seconds. On a change it restores the old service before configuring the newly active one.
- On the **next Agent launch** after a crash, it attempts recovery from the journal before enabling the configured system proxy again.
- It only restores a proxy if the current configuration still matches RelayProxy's own applied values (or a recognized interrupted write). If another program or the user has changed the proxy, the newer value is preserved rather than overwritten.
- Failures retain pending journal entries and surface a warning. Do not manually delete a journal with pending recovery unless the system settings have been independently inspected.

Known limitations: there is **no independent root watchdog** to restore immediately when an Agent crashes and remains stopped. A pending recovery may require interactive administrator authorization. `networksetup` can disable a previously empty proxy but may leave a disabled server/port entry; no active proxy is left on in that case. Multi-route or VPN default interfaces without a corresponding network service may not be managed automatically. Exact restoration of key absence is not yet guaranteed.

## Acceptance checklist (manual on macOS)

1. Record `networksetup -getwebproxy "Wi-Fi"`, `-getsecurewebproxy`, and `-getsocksfirewallproxy`; enable system mode and confirm loopback ports.
2. Verify HTTP, HTTPS CONNECT, domain routing, DIRECT / PROXY / REJECT and selected exit behavior.
3. Disable the mode and inspect restored enabled/host/port values.
4. Test previously configured disabled/active proxies. Confirm an authenticated proxy or active PAC is refused without mutation.
5. Switch Wi-Fi to Ethernet; ensure old service restored before applying the new one.
6. Change proxy settings using another app while RelayProxy is active; ensure they are not clobbered on shutdown.
7. Force-kill Agent, restart it and verify journal recovery; test a rejected administrator prompt.
8. Verify the browser/Web UI and Agent loopback connections remain accessible, and verify that uncaptured UDP/QUIC is labeled as a system-proxy limitation.

Automated tests use a fake network backend; these checks **do not** establish that macOS authorization and every network-service variant work on a real machine.
