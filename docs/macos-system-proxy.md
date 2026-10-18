# macOS system HTTP/HTTPS/SOCKS proxy (without Developer ID)

This opt-in mode is independent of `network.mode=divert` and does not install a Network Extension, a TUN device, or a privileged background service.

## Configuration

```yaml
system_proxy:
  mode: system
  auto_follow: true
  http: true
  socks5: true
  proxy_ip_literals: true
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

The manager identifies the active network service using `route -n get default` and `networksetup -listnetworkserviceorder`. On the active service, it configures HTTP, Secure Web Proxy, and SOCKS to the Agent's loopback listeners. It doesn't alter PAC, automatic discovery, or DNS settings. When `proxy_ip_literals: true`, it temporarily removes non-loopback IP/CIDR rules from the macOS *Bypass proxy settings for these hosts & domains* list. Non-IP exceptions, loopback and link-local exceptions are retained; the original bypass list is journaled before changes and restored on shutdown or next launch. Set `proxy_ip_literals: false` to leave the bypass list untouched. Active authenticated proxies and active PAC/auto-discovery settings are treated as configuration conflicts and are not overridden.

When `networksetup` reports insufficient privileges, the GUI may trigger an administrator prompt via `osascript`. The helper runs only fixed `/usr/sbin/networksetup` commands with shell-escaped arguments. No root process or persistent privileged helper is installed.

## Persistence, recovery and conflicts

Before changing each proxy type, the manager writes a mode-0600 JSON journal alongside the Agent YAML file (`macos-system-proxy-recovery.json`). It records the network service, the previous effective enabled/server/port state, and the Agent's expected state. For IP targets it also stores the original bypass list and RelayProxy's applied list.

- On a normal shutdown or when switching off system proxy mode, RelayProxy restores prior settings.
- With `auto_follow: true`, it polls the default network service every seven seconds. On a change it restores the old service before configuring the newly active one.
- On the **next Agent launch** after a crash, it attempts recovery from the journal before enabling the configured system proxy again.
- It only restores a proxy or bypass list if the current configuration still matches RelayProxy's own applied values (or a recognized interrupted write). If another program or the user has changed the proxy, the newer value is preserved rather than overwritten.
- Failures retain pending journal entries and surface a warning. Do not manually delete a journal with pending recovery unless the system settings have been independently inspected.

Known limitations: there is **no independent root watchdog** to restore immediately when an Agent crashes and remains stopped. A pending recovery may require interactive administrator authorization. `networksetup` can disable a previously empty proxy but may leave a disabled server/port entry; no active proxy is left on in that case. Multi-route or VPN default interfaces without a corresponding network service may not be managed automatically. Exact restoration of key absence is not yet guaranteed.

## Acceptance checklist (manual on macOS)

1. Record `networksetup -getwebproxy "Wi-Fi"`, `-getsecurewebproxy`, and `-getsocksfirewallproxy`; enable system mode and confirm loopback ports.
2. Verify ordinary HTTP, HTTPS CONNECT, IPv4/IPv6 literal targets with explicit nonstandard ports, domain routing, DIRECT / PROXY / REJECT and selected exit behavior. Inspect `networksetup -getproxybypassdomains "Wi-Fi"` before, during and after. Confirm safe preservation of existing `localhost`/`*.local` bypass rules.
3. Disable the mode and inspect restored enabled/host/port values.
4. Test previously configured disabled/active proxies. Confirm an authenticated proxy or active PAC is refused without mutation.
5. Switch Wi-Fi to Ethernet; ensure old service restored before applying the new one.
6. Change proxy settings using another app while RelayProxy is active; ensure they are not clobbered on shutdown.
7. Force-kill Agent, restart it and verify journal recovery; test a rejected administrator prompt.
8. Verify the browser/Web UI and Agent loopback connections remain accessible, and verify that uncaptured UDP/QUIC is labeled as a system-proxy limitation.

Automated tests use a fake network backend; these checks **do not** establish that macOS authorization and every network-service variant work on a real machine.

## IP:port troubleshooting

For URLs such as `http://192.168.1.10:8088` or `https://203.0.113.10:8443`, the Agent HTTP/SOCKS listeners and routing engine accept IP literals. If the request never appears in RelayProxy traffic monitoring, inspect the macOS network-service bypass list and confirm `proxy_ip_literals: true` and that the application uses system proxy settings. The new setting removes explicit routable IP bypass patterns; it cannot override application-specific bypass behavior or a custom non-HTTP raw TCP socket.

Check the client path using a known, authorized target:

```sh
networksetup -getproxybypassdomains "Wi-Fi"
scutil --proxy
curl -v --noproxy "" --proxy http://127.0.0.1:8080 http://<reachable-ip>:<port>/
curl -v --noproxy "" --proxy http://127.0.0.1:8080 https://<reachable-ip>:<port>/
```

Do **not** paste `<reachable-ip>` literally; substitute an IP and port that you administer. If the explicit proxy command works but the application's original IP URL does not, the app is bypassing system proxy. If the explicit proxy command fails and a flow appears in the Agent logs, diagnose exit authorization and the corresponding route or ACL. Private IP ranges on a **remote** exit may require explicitly enabling `allow_private_network` on that exit; this setting is not changed by the IP bypass feature.

By default, Chromium and some other applications deliberately bypass `localhost`, loopback and link-local addresses even if no bypass list is set. RelayProxy intentionally preserves those safety exclusions. Proxying arbitrary non-HTTP sockets requires TUN/Network Extension, not system HTTP/SOCKS settings.
