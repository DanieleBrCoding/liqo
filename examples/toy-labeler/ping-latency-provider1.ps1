# Ping RTT from a pod scheduled on provider1 to a pod on provider2 via the hub-and-spoke path.
param(
  [int]$Count = 20,
  [string]$Namespace = "hub-spoke-demo",
  [string]$ClientOnProvider1 = "client-on-provider1",
  [string]$ClientOnProvider2 = "client-on-provider2",
  [ValidateSet("Service", "PodIP")]
  [string]$TargetMode = "Service",
  [string]$ServiceName = "demo-server",
  [switch]$VerifyIndirect,
  [switch]$TracePath,
  [switch]$CaptureGateway,
  [switch]$ConfirmPath,
  [string]$CaptureFilter = "",
  [int]$CaptureCount = 6,
  [int]$CaptureTimeoutSeconds = 15
)

$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Definition
$KC_CONSUMER = Join-Path $here "liqo_kubeconf_consumer"
$KC_PROVIDER1 = Join-Path $here "liqo_kubeconf_provider1"
$KC_PROVIDER2 = Join-Path $here "liqo_kubeconf_provider2"

if ($ConfirmPath) {
  $TracePath = $true
  $VerifyIndirect = $true
  $CaptureGateway = $true
}

if (-not (Test-Path $KC_CONSUMER)) {
  Write-Error "Missing kubeconfig: $KC_CONSUMER. Run setup-liqo-minimal.ps1 first."
  exit 1
}

function Ensure-ClientOnProvider1 {
  param(
    [string]$Kubeconfig,
    [string]$Ns,
    [string]$PodName
  )

  $exists = kubectl --kubeconfig $Kubeconfig -n $Ns get pod $PodName --ignore-not-found -o name
  if (-not [string]::IsNullOrWhiteSpace($exists)) {
    return
  }

  $yaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: $PodName
  namespace: $Ns
spec:
  containers:
    - name: netshoot
      image: nicolaka/netshoot:latest
      command: ["sleep", "infinity"]
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
          - matchExpressions:
              - key: liqo.io/remote-cluster-id
                operator: In
                values:
                  - provider1
"@

  $yaml | kubectl --kubeconfig $Kubeconfig apply -f - | Out-Null
  kubectl --kubeconfig $Kubeconfig -n $Ns wait --for=condition=Ready pod/$PodName --timeout=120s | Out-Null
}

function Get-PodRemoteClusterId {
  param(
    [string]$Kubeconfig,
    [string]$Ns,
    [string]$PodName
  )

  $nodeName = kubectl --kubeconfig $Kubeconfig -n $Ns get pod $PodName -o jsonpath="{.spec.nodeName}"
  if (-not $nodeName) {
    return ""
  }

  $remoteId = kubectl --kubeconfig $Kubeconfig get node $nodeName -o jsonpath="{.metadata.labels.liqo\.io/remote-cluster-id}"
  return $remoteId
}

function Get-PodInfo {
  param(
    [string]$Kubeconfig,
    [string]$Ns,
    [string]$PodName
  )

  $podJson = kubectl --kubeconfig $Kubeconfig -n $Ns get pod $PodName -o json
  if ($LASTEXITCODE -ne 0 -or -not $podJson) {
    return $null
  }

  $pod = $podJson | ConvertFrom-Json
  $nodeName = $pod.spec.nodeName
  $podIp = $pod.status.podIP
  $remoteId = ""
  if ($nodeName) {
    $remoteId = kubectl --kubeconfig $Kubeconfig get node $nodeName -o jsonpath="{.metadata.labels.liqo\.io/remote-cluster-id}"
  }

  return [pscustomobject]@{
    Pod = $PodName
    PodIP = $podIp
    Node = $nodeName
    RemoteClusterId = $remoteId
  }
}

function Find-GatewayPod {
  param(
    [string]$Kubeconfig,
    [string]$Ns
  )

  $podName = kubectl --kubeconfig $Kubeconfig -n $Ns get pods -o name | Select-String "gw" | Select-Object -First 1
  if (-not $podName) {
    return ""
  }
  return $podName.ToString().Replace("pod/", "")
}

function Get-ServiceAnnotation {
  param(
    [string]$Kubeconfig,
    [string]$Ns,
    [string]$Name,
    [string]$Key
  )

  $jsonPath = "{.metadata.annotations['$Key']}"
  $value = kubectl --kubeconfig $Kubeconfig -n $Ns get svc $Name --ignore-not-found -o jsonpath="$jsonPath"
  return $value
}

# Ensure the source pod exists and is ready (scheduled on provider1 via consumer API).
Ensure-ClientOnProvider1 -Kubeconfig $KC_CONSUMER -Ns $Namespace -PodName $ClientOnProvider1

# Target must exist from the demo (client on provider2).
$targetIP = kubectl --kubeconfig $KC_CONSUMER -n $Namespace get pod $ClientOnProvider2 --ignore-not-found -o jsonpath="{.status.podIP}"
if (-not $targetIP) {
  Write-Error "Target pod '$ClientOnProvider2' not found in namespace '$Namespace'. Run demo-hub-spoke.ps1 first."
  exit 1
}

$serviceFqdn = "$ServiceName.$Namespace.svc.cluster.local"
$svcAnno = Get-ServiceAnnotation -Kubeconfig $KC_CONSUMER -Ns $Namespace -Name $ServiceName -Key "use-direct-connections"
if ($ConfirmPath -and $TargetMode -eq "Service" -and $svcAnno -eq "true") {
  Write-Error "Service '$ServiceName' has use-direct-connections=true. Remove the annotation to force the indirect path."
  exit 1
}

$srcInfo = $null
$dstInfo = $null
$gwP1 = ""
$gwP2 = ""

if ($TracePath -or $CaptureGateway) {
  $srcInfo = Get-PodInfo -Kubeconfig $KC_CONSUMER -Ns $Namespace -PodName $ClientOnProvider1
  $dstInfo = Get-PodInfo -Kubeconfig $KC_CONSUMER -Ns $Namespace -PodName $ClientOnProvider2

  if (-not $srcInfo -or -not $dstInfo) {
    Write-Error "Unable to resolve pod info for path tracing."
    exit 1
  }

  $gwP1 = Find-GatewayPod -Kubeconfig $KC_CONSUMER -Ns "liqo-tenant-provider1"
  $gwP2 = Find-GatewayPod -Kubeconfig $KC_CONSUMER -Ns "liqo-tenant-provider2"
}

if ($TracePath) {
  Write-Host "[MAP] podIP $($srcInfo.PodIP) -> cluster $($srcInfo.RemoteClusterId)" -ForegroundColor Cyan
  Write-Host "[MAP] podIP $($dstInfo.PodIP) -> cluster $($dstInfo.RemoteClusterId)" -ForegroundColor Cyan
  Write-Host "[PATH] source pod=$($srcInfo.Pod) ip=$($srcInfo.PodIP) node=$($srcInfo.Node) cluster=$($srcInfo.RemoteClusterId)" -ForegroundColor Cyan
  Write-Host "[PATH] target pod=$($dstInfo.Pod) ip=$($dstInfo.PodIP) node=$($dstInfo.Node) cluster=$($dstInfo.RemoteClusterId)" -ForegroundColor Cyan

  if ($TargetMode -eq "Service") {
    Write-Host "[PATH] service=$serviceFqdn (use-direct-connections=$svcAnno)" -ForegroundColor Cyan
  }

  $svcIp = kubectl --kubeconfig $KC_CONSUMER -n $Namespace get svc demo-server --ignore-not-found -o jsonpath="{.spec.clusterIP}"
  if ($svcIp) {
    Write-Host "[PATH] service demo-server clusterIP=$svcIp" -ForegroundColor Cyan
  }

  if ($gwP1) {
    Write-Host "[PATH] consumer gateway (tenant-provider1): $gwP1" -ForegroundColor Cyan
    Write-Host "[HINT] Run to observe ICMP on consumer gateway (provider1 side):" -ForegroundColor Yellow
    Write-Host "kubectl --kubeconfig $KC_CONSUMER -n liqo-tenant-provider1 exec $gwP1 -- tcpdump -i any -n icmp and host $($dstInfo.PodIP)" -ForegroundColor Yellow
  }

  if ($gwP2) {
    Write-Host "[PATH] consumer gateway (tenant-provider2): $gwP2" -ForegroundColor Cyan
    Write-Host "[HINT] Run to observe ICMP on consumer gateway (provider2 side):" -ForegroundColor Yellow
    Write-Host "kubectl --kubeconfig $KC_CONSUMER -n liqo-tenant-provider2 exec $gwP2 -- tcpdump -i any -n icmp and host $($srcInfo.PodIP)" -ForegroundColor Yellow
  }
}

$tcpJobs = @()
if ($CaptureGateway) {
  if (-not $gwP1 -or -not $gwP2) {
    Write-Host "[WARN] Gateway pod not found; cannot capture on consumer." -ForegroundColor Yellow
  } else {
    $filterP1 = "icmp"
    $filterP2 = "icmp"
    if ($TargetMode -eq "Service") {
      $filterP1 = "tcp and port 80"
      $filterP2 = "tcp and port 80"
    }
    if ($CaptureFilter) {
      $filterP1 = $CaptureFilter
      $filterP2 = $CaptureFilter
    }

    $tcpJobs += Start-Job -ScriptBlock {
      param($kc, $ns, $pod, $filter, $count)
      kubectl --kubeconfig $kc -n $ns exec $pod -- tcpdump -i any -n -c $count $filter 2>&1
    } -ArgumentList $KC_CONSUMER, "liqo-tenant-provider1", $gwP1, $filterP1, $CaptureCount

    $tcpJobs += Start-Job -ScriptBlock {
      param($kc, $ns, $pod, $filter, $count)
      kubectl --kubeconfig $kc -n $ns exec $pod -- tcpdump -i any -n -c $count $filter 2>&1
    } -ArgumentList $KC_CONSUMER, "liqo-tenant-provider2", $gwP2, $filterP2, $CaptureCount

    Start-Sleep -Seconds 2
  }
}

if ($VerifyIndirect) {
  $srcCluster = Get-PodRemoteClusterId -Kubeconfig $KC_CONSUMER -Ns $Namespace -PodName $ClientOnProvider1
  $dstCluster = Get-PodRemoteClusterId -Kubeconfig $KC_CONSUMER -Ns $Namespace -PodName $ClientOnProvider2

  if ($srcCluster -ne "provider1") {
    Write-Error "Source pod '$ClientOnProvider1' is not scheduled on provider1 (found '$srcCluster')."
    exit 1
  }

  if ($dstCluster -ne "provider2") {
    Write-Error "Target pod '$ClientOnProvider2' is not scheduled on provider2 (found '$dstCluster')."
    exit 1
  }

  if ((Test-Path $KC_PROVIDER1) -and (Test-Path $KC_PROVIDER2)) {
    $directP1 = kubectl --kubeconfig $KC_PROVIDER1 -n liqo-tenant-provider2 get connections.networking.liqo.io gw-provider2 --ignore-not-found -o jsonpath="{.status.value}"
    $directP2 = kubectl --kubeconfig $KC_PROVIDER2 -n liqo-tenant-provider1 get connections.networking.liqo.io gw-provider1 --ignore-not-found -o jsonpath="{.status.value}"

    if ($directP1 -or $directP2) {
      if ($TargetMode -eq "PodIP") {
        Write-Host "[WARN] Direct provider-provider connection detected (p1=$directP1, p2=$directP2). For pod-to-pod ping this can bypass the consumer." -ForegroundColor Yellow
        if ($ConfirmPath) {
          Write-Error "Direct provider-provider tunnel is present. Disconnect it to force the indirect path: liqoctl network disconnect --kubeconfig $KC_PROVIDER1 --remote-kubeconfig $KC_PROVIDER2"
          exit 1
        }
      } else {
        Write-Host "[INFO] Direct provider-provider tunnel present, but service traffic uses it only if use-direct-connections=true." -ForegroundColor Cyan
      }
    } else {
      Write-Host "[OK] No direct provider-provider connection found. Traffic must go via consumer." -ForegroundColor Green
    }
  } else {
    Write-Host "[WARN] Provider kubeconfigs not found; cannot verify direct connection status." -ForegroundColor Yellow
  }
}

# Run the request from the pod on provider1 to the target.
if ($TargetMode -eq "Service") {
  $times = @()
  for ($i = 1; $i -le $Count; $i++) {
    $t = kubectl --kubeconfig $KC_CONSUMER -n $Namespace exec $ClientOnProvider1 -c netshoot -- curl -s -o /dev/null -w "%{time_total}" "http://$serviceFqdn"
    if ([string]::IsNullOrWhiteSpace($t)) {
      Write-Error "Empty timing from curl; check that the service is reachable."
      exit 1
    }
    $times += [double]$t
  }

  $avgSec = ($times | Measure-Object -Average).Average
  $avgMs = [math]::Round($avgSec * 1000, 3)
  Write-Output "avg_ms=$avgMs"
} else {
  $pingOut = kubectl --kubeconfig $KC_CONSUMER -n $Namespace exec $ClientOnProvider1 -c netshoot -- ping -c $Count $targetIP
  $pingOut

  # Extract avg RTT from the summary line.
  $summary = $pingOut | Select-String "rtt|round-trip"
  if ($summary) {
    $avg = $summary.Line.Split("=")[1].Trim().Split("/")[1]
    Write-Output "avg_ms=$avg"
  }
}

if ($CaptureGateway -and $tcpJobs.Count -gt 0) {
  foreach ($job in $tcpJobs) {
    if (-not (Wait-Job -Job $job -Timeout $CaptureTimeoutSeconds)) {
      Stop-Job -Job $job | Out-Null
      Remove-Job -Job $job | Out-Null
      Write-Host "[WARN] tcpdump capture timed out. Try increasing -CaptureTimeoutSeconds or removing the host filter." -ForegroundColor Yellow
      continue
    }

    $out = Receive-Job -Job $job
    Remove-Job -Job $job | Out-Null
    if ($out) {
      Write-Host "[CAPTURE] tcpdump output:" -ForegroundColor Green
      $out
    } else {
      Write-Host "[WARN] tcpdump returned no packets. Try rerun with -CaptureCount 2 and lower ping count." -ForegroundColor Yellow
    }
  }
}

