#requires -Version 5.1
[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`""
    Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Verb RunAs -ArgumentList $arguments
    exit
}

try {
    Write-Host 'EchoManager WinRM HTTPS setup' -ForegroundColor Cyan
    Write-Host 'This configures remote PowerShell over HTTPS and adds a port 5986 firewall rule for the Manager host.'
    Write-Host 'Pre-existing firewall rules may also allow port 5986; this script leaves them unchanged.' -ForegroundColor Yellow
    Write-Host 'Run this only on a trusted Private or Domain network.'

    $managerAddressText = Read-Host 'Enter the EchoManager host IPv4 address'
    [System.Net.IPAddress]$managerAddress = [System.Net.IPAddress]::Any
    if (-not [System.Net.IPAddress]::TryParse($managerAddressText, [ref]$managerAddress)) {
        throw 'The Manager address must be a valid IPv4 address.'
    }
    if ($managerAddress.AddressFamily -ne [System.Net.Sockets.AddressFamily]::InterNetwork -or
        $managerAddress.Equals([System.Net.IPAddress]::Any) -or
        [System.Net.IPAddress]::IsLoopback($managerAddress)) {
        throw 'Enter a non-loopback IPv4 address for the Manager host.'
    }

    $profiles = @(Get-NetConnectionProfile)
    if (-not ($profiles | Where-Object { $_.NetworkCategory -in @('Private', 'DomainAuthenticated') })) {
        throw 'No Private or Domain network profile is active. Set the trusted LAN profile to Private, then run this script again.'
    }

    Set-Service -Name WinRM -StartupType Automatic
    Start-Service -Name WinRM

    $httpListenersBefore = @(
        Get-ChildItem WSMan:\localhost\Listener |
            Where-Object { $_.Keys -contains 'Transport=HTTP' }
    )
    $httpRulesBefore = @{}
    Get-NetFirewallRule -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like 'WINRM-HTTP-In-TCP*' -and $_.Enabled -eq 'True' } |
        ForEach-Object { $httpRulesBefore[$_.Name] = $true }

    Enable-PSRemoting -Force

    $httpsListeners = @(
        Get-ChildItem WSMan:\localhost\Listener |
            Where-Object { $_.Keys -contains 'Transport=HTTPS' }
    )
    if ($httpsListeners.Count -eq 0) {
        $certificate = New-SelfSignedCertificate `
            -DnsName $env:COMPUTERNAME `
            -CertStoreLocation 'Cert:\LocalMachine\My' `
            -Type SSLServerAuthentication `
            -NotAfter (Get-Date).AddYears(2)

        New-Item -Path WSMan:\localhost\Listener `
            -Transport HTTPS `
            -Address * `
            -CertificateThumbPrint $certificate.Thumbprint `
            -Force | Out-Null
    }

    # EchoManager uses Basic authentication only inside the HTTPS connection.
    Set-Item -Path WSMan:\localhost\Service\Auth\Basic -Value $true

    $ruleName = 'EchoManager-WinRM-HTTPS'
    Get-NetFirewallRule -Name $ruleName -ErrorAction SilentlyContinue |
        Remove-NetFirewallRule
    New-NetFirewallRule `
        -Name $ruleName `
        -DisplayName 'EchoManager WinRM HTTPS' `
        -Direction Inbound `
        -Action Allow `
        -Protocol TCP `
        -LocalPort 5986 `
        -RemoteAddress ($managerAddress.ToString()) `
        -Profile Private, Domain | Out-Null

    # Enable-PSRemoting creates an HTTP listener on a fresh setup. Remove only
    # the listener and firewall rules that this invocation newly enabled.
    if ($httpListenersBefore.Count -eq 0) {
        Get-ChildItem WSMan:\localhost\Listener |
            Where-Object { $_.Keys -contains 'Transport=HTTP' } |
            ForEach-Object { Remove-Item -Path $_.PSPath -Recurse -Force -Confirm:$false }
    }
    Get-NetFirewallRule -ErrorAction SilentlyContinue |
        Where-Object {
            $_.Name -like 'WINRM-HTTP-In-TCP*' -and
            $_.Enabled -eq 'True' -and
            -not $httpRulesBefore.ContainsKey($_.Name)
        } |
        Disable-NetFirewallRule

    $httpsListeners = @(
        Get-ChildItem WSMan:\localhost\Listener |
            Where-Object { $_.Keys -contains 'Transport=HTTPS' }
    )
    if ($httpsListeners.Count -eq 0) {
        throw 'HTTPS listener setup did not complete.'
    }
    if (-not (Get-NetTCPConnection -LocalPort 5986 -State Listen -ErrorAction SilentlyContinue)) {
        throw 'The HTTPS listener exists, but TCP port 5986 is not listening.'
    }

    Write-Host ''
    Write-Host 'WinRM HTTPS is ready on port 5986.' -ForegroundColor Green
    Write-Host "Firewall source: $($managerAddress.ToString())"
    Write-Host 'Review existing inbound firewall rules for TCP 5986 if this PC was previously configured for WinRM HTTPS.' -ForegroundColor Yellow
    Write-Host 'Basic authentication is enabled; unencrypted WinRM traffic was not enabled.'
    Write-Host 'A self-signed certificate is used only when no HTTPS listener existed.'
    Write-Host 'In EchoManager, use Windows / HTTPS WinRM / port 5986.'
    Write-Host 'For a self-signed certificate, enable certificate bypass only on a trusted LAN; otherwise install a trusted certificate matching the device address.' -ForegroundColor Yellow

    if ($httpListenersBefore.Count -gt 0) {
        Write-Host 'An HTTP listener existed before setup and was left unchanged.' -ForegroundColor Yellow
    } else {
        Write-Host 'The HTTP listener created during setup was removed; EchoManager uses HTTPS only.'
    }
} catch {
    Write-Host "Setup failed: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host 'This script does not create a firewall allow rule for the whole network. Review existing rules and the error before rerunning.' -ForegroundColor Yellow
    Read-Host 'Press Enter to close'
    exit 1
}

Read-Host 'Setup complete. Press Enter to close'
