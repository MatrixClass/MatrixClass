param(
    [string]$ChannelId,
    [string]$TeacherAddress,
    [int]$Port = 0,
    [string]$OutputFile
)

$ErrorActionPreference = "Stop"
$report = [ordered]@{
    Timestamp = (Get-Date).ToUniversalTime().ToString("o")
    Host = $env:COMPUTERNAME
    Platform = "Windows host diagnostic; run the POSIX script on macOS for native results"
    ChannelId = $ChannelId
    TeacherAddress = $TeacherAddress
    Port = $Port
    Interfaces = @()
    Routes = @()
    Dns = @()
    Reachability = @()
}
$report.Interfaces = @(Get-NetIPConfiguration | ForEach-Object { [ordered]@{ Interface = $_.InterfaceAlias; Index = $_.InterfaceIndex; IPv4 = @($_.IPv4Address.IPAddress); IPv6 = @($_.IPv6Address.IPAddress); Gateway = @($_.IPv4DefaultGateway.NextHop); Dns = @($_.DNSServer.ServerAddresses) } })
$report.Routes = @(Get-NetRoute -AddressFamily IPv4 | Where-Object { $_.State -eq "Alive" } | Select-Object InterfaceAlias, DestinationPrefix, NextHop, RouteMetric)
$report.Dns = @(Get-DnsClientServerAddress -AddressFamily IPv4 | Select-Object InterfaceAlias, ServerAddresses)
if ($TeacherAddress) {
    $reachability = [ordered]@{ Target = $TeacherAddress; Ping = (Test-Connection -ComputerName $TeacherAddress -Count 2 -Quiet -ErrorAction SilentlyContinue) }
    if ($Port -gt 0) { $tcp = Test-NetConnection -ComputerName $TeacherAddress -Port $Port -WarningAction SilentlyContinue; $reachability.TcpPort = $Port; $reachability.TcpSucceeded = $tcp.TcpTestSucceeded }
    $report.Reachability += $reachability
}
$json = $report | ConvertTo-Json -Depth 8
if ($OutputFile) { $json | Set-Content -LiteralPath $OutputFile -Encoding utf8 }
$json
