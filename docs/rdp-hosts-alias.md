# RDP device names in the local hosts file

When an approved RDP device is connected, RelayProxy keeps its local TCP/UDP
listener bound to `127.0.0.1:<stable-device-port>` and tries to install a
user-friendly hosts alias pointing to `127.0.0.1`.

For example, device name `Office PC` might produce:

```text
127.0.0.1 relayproxy-office-pc-a1b2c3d4 # RelayProxy RDP
```

The generated hash is derived from the approved device ID, keeping identically
named machines independent. A non-ASCII device name is encoded as a DNS-safe
hostname. The RDP client opens `relayproxy-office-pc-a1b2c3d4:<local port>`.
The operating system's hosts file maps **only the hostname**; the port remains
necessary because different remote devices have different loopback ports.

## Permissions and safety

* Windows hosts file: `%SystemRoot%\System32\drivers\etc\hosts`. If the
  Agent cannot append to it, Windows requests elevation through UAC. The native ShellExecuteExW helper
  stays hidden (no PowerShell/console window); only the UAC consent dialog appears.
* macOS and Linux: `/etc/hosts`. macOS may show the system administrator
  authorization prompt; Linux uses `pkexec` where available.
* Only the reserved `relayproxy-<device>-<hash>` names, with a deterministic device-ID
  suffix, are installed. Other lines and comments are preserved. If the name
  already points at another IP, RelayProxy **does not overwrite** it.
* Hosts entries persist after RDP disconnect and Agent restart to avoid repeated
  privileged prompts. On next connection, a previous RelayProxy-owned `*.relayproxy.test`
  hosts line for that device is migrated to the suffix-free name without
  touching unrelated hosts lines. Rename or remove stale RelayProxy-tagged entries manually
  if a device is permanently removed. Automatic deletion is deliberately not
  attempted without a safe admin-authorized cleanup flow.
* On failed or refused elevation, the existing tunnel **still works** through
  `127.0.0.1:<port>`. The Agent logs the error and the UI shows a warning.
* Creating the alias does **not** grant RDP access or change server-side
  permissions; the controller still verifies that the target is authorized and
  online before establishing its local RDP tunnel.

## Verification

1. Connect an approved RDP target in the Agent GUI.
2. Confirm that the displayed local address is a `relayproxy-<device>-<hash>:<port>` URL
   and that the OS hosts file contains a RelayProxy-tagged line.
3. Resolve the hostname; it should resolve to `127.0.0.1`.
4. Connect another device; verify each alias has a distinct local port.
5. Deny UAC/admin authorization for a previously unseen device and verify that
   the RDP connection still opens through `127.0.0.1:<port>`.
