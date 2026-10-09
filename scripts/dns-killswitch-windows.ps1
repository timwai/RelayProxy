# Independent, opt-in Windows DNS leak protection using persistent WFP rules.
# WARNING: Firewall ordering relative to WinDivert must be validated on each
# Windows version. Blocking outbound UDP/TCP 53 may prevent FakeIP synthetic
# responses if WinDivert cannot capture the query before Windows Firewall.
# This is not an HTTP/443 DoH kill switch and should be tested offline first.
param(
    [Parameter(Position = 0)]
    [ValidateSet('Enable','Disable','Status')]
    [string] $Action = 'Status'
)
$ErrorActionPreference = 'Stop'
$prefix = 'RelayProxy-DNS-KillSwitch'
$ports = @('53','853','784','8853')
$names = @("$prefix-TCP","$prefix-UDP")

function Test-Administrator {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

if ($Action -ne 'Status' -and -not (Test-Administrator)) {
    throw 'Open an elevated PowerShell terminal to change DNS protection.'
}
switch ($Action) {
    'Enable' {
        foreach ($protocol in @('TCP','UDP')) {
            $ruleName = "$prefix-$protocol"
            if (-not (Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue)) {
                New-NetFirewallRule -Name $ruleName -DisplayName "RelayProxy persistent DNS guard ($protocol)" `
                    -Direction Outbound -Action Block -Protocol $protocol -RemotePort $ports `
                    -Profile Any -Enabled True -PolicyStore PersistentStore | Out-Null
            } else {
                Set-NetFirewallRule -Name $ruleName -Enabled True -Action Block
            }
        }
        Write-Host 'Persistent DNS blocking rules enabled. Test Agent DNS connectivity and rollback if necessary.'
    }
    'Disable' {
        foreach ($ruleName in $names) {
            Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue |
                Remove-NetFirewallRule -ErrorAction Stop
        }
        Write-Host 'RelayProxy DNS guard rules removed.'
    }
    'Status' {
        $found = @(foreach ($ruleName in $names) {
            Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue
        })
        $found | Format-Table -Property Name, DisplayName, Enabled, Action, PolicyStoreSourceType
        if ($found.Count -ne 2 -or @($found | Where-Object { $_.Enabled -ne 'True' }).Count -gt 0) {
            Write-Warning 'Windows DNS guard is not fully installed/enabled.'
        }
    }
}
