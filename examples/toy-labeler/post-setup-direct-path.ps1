# post-setup-direct-path.ps1
# Post-setup helper to enforce patched virtual-kubelet rollout and validate direct path behavior.

param(
    [string]$LiqoVersion = "v1.0.1",
    [switch]$SkipBuild,
    [switch]$SkipTrafficCheck
)

$ErrorActionPreference = "Stop"

function Write-Info($m) { Write-Host "[INFO]    $m" -ForegroundColor Cyan }
function Write-Ok($m)   { Write-Host "[SUCCESS] $m" -ForegroundColor Green }
function Write-Err($m)  { Write-Host "[ERROR]   $m" -ForegroundColor Red }

$here = Split-Path -Parent $MyInvocation.MyCommand.Definition
$root = Resolve-Path (Join-Path $here "..\..")

$kcCons = Join-Path $here "liqo_kubeconf_consumer"
$kcP1   = Join-Path $here "liqo_kubeconf_provider1"
$kcP2   = Join-Path $here "liqo_kubeconf_provider2"

if (!(Test-Path $kcCons) -or !(Test-Path $kcP1) -or !(Test-Path $kcP2)) {
    Write-Err "Kubeconfig mancanti in $here. Esegui prima setup-liqo-minimal.ps1"
    exit 1
}

$image = "ghcr.io/liqotech/virtual-kubelet:$LiqoVersion"

Write-Info "=== STEP 1: Build/prepare virtual-kubelet image ($image) ==="
if (-not $SkipBuild) {
    Push-Location $root
    try {
        $env:GOOS = "linux"
        $env:GOARCH = "amd64"
        $env:CGO_ENABLED = "0"

        New-Item -ItemType Directory -Force -Path "bin\amd64" | Out-Null
        go build -ldflags="-s -w" -o "bin/amd64/virtual-kubelet_linux_amd64" "./cmd/virtual-kubelet"
        if ($LASTEXITCODE -ne 0) { throw "go build virtual-kubelet failed" }

        docker build --build-arg COMPONENT=virtual-kubelet -t $image -f build/liqo/Dockerfile .
        if ($LASTEXITCODE -ne 0) { throw "docker build failed" }
    }
    finally {
        Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
        Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue
        Remove-Item Env:\CGO_ENABLED -ErrorAction SilentlyContinue
        Pop-Location
    }
    Write-Ok "Immagine buildata: $image"
} else {
    Write-Info "Skip build richiesto. Uso immagine locale già presente: $image"
}

Write-Info "=== STEP 2: Load image into kind clusters ==="
foreach ($c in @("consumer", "provider1", "provider2")) {
    kind load docker-image $image --name $c
    if ($LASTEXITCODE -ne 0) { Write-Err "kind load failed for cluster $c"; exit 1 }
}
Write-Ok "Immagine caricata in consumer/provider1/provider2"

Write-Info "=== STEP 3: Restart virtual-kubelet pods on consumer ==="
$vkPods = kubectl get pods -A --kubeconfig $kcCons --no-headers | Select-String "vk-provider"
if (-not $vkPods) {
    Write-Err "Nessun pod vk-provider trovato sul consumer"
    exit 1
}

foreach ($line in $vkPods) {
    $parts = ($line.ToString() -split "\s+")
    $ns = $parts[0]
    $name = $parts[1]
    Write-Info "Delete pod $ns/$name"
    kubectl delete pod $name -n $ns --kubeconfig $kcCons | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Err "Delete pod failed for $ns/$name"; exit 1 }
}

Start-Sleep -Seconds 4

$vkPodsAfter = kubectl get pods -A --kubeconfig $kcCons --no-headers | Select-String "vk-provider"
if (-not $vkPodsAfter) {
    Write-Err "Nessun pod vk-provider trovato dopo restart"
    exit 1
}

foreach ($line in $vkPodsAfter) {
    $parts = ($line.ToString() -split "\s+")
    $ns = $parts[0]
    $name = $parts[1]
    kubectl wait --for=condition=Ready pod/$name -n $ns --kubeconfig $kcCons --timeout=180s | Out-Null
    if ($LASTEXITCODE -ne 0) { Write-Err "Pod non pronto: $ns/$name"; exit 1 }
}
Write-Ok "Pod virtual-kubelet riavviati e pronti"

Write-Info "=== STEP 4: Ensure provider1<->provider2 direct tunnel ==="
liqoctl network connect --kubeconfig $kcP1 --remote-kubeconfig $kcP2 --gw-server-service-type NodePort
if ($LASTEXITCODE -ne 0) { Write-Err "liqoctl network connect failed"; exit 1 }
Write-Ok "Connessione diretta provider1<->provider2 richiesta"

Write-Info "=== STEP 5: Verify runtime image IDs on vk pods ==="
$vkPodsFinal = kubectl get pods -A --kubeconfig $kcCons --no-headers | Select-String "vk-provider"
foreach ($line in $vkPodsFinal) {
    $parts = ($line.ToString() -split "\s+")
    $ns = $parts[0]
    $name = $parts[1]
    $imageID = kubectl get pod $name -n $ns --kubeconfig $kcCons -o jsonpath='{.status.containerStatuses[0].imageID}'
    Write-Host "$ns/$name => $imageID"
}

if ($SkipTrafficCheck) {
    Write-Info "Skip traffic check richiesto. Fine."
    exit 0
}

Write-Info "=== STEP 6: Optional A/B traffic check (if demo resources exist) ==="
$svcExists = kubectl get svc demo-server -n hub-spoke-demo --kubeconfig $kcCons --ignore-not-found
if (-not $svcExists) {
    Write-Info "Service demo-server non trovato. Salto traffic check."
    exit 0
}

$clientExists = kubectl get pod client-on-provider2 -n hub-spoke-demo --kubeconfig $kcCons --ignore-not-found
if (-not $clientExists) {
    Write-Info "Pod client-on-provider2 non trovato. Salto traffic check."
    exit 0
}

$gwLine = kubectl get pods -n liqo-tenant-provider1 --kubeconfig $kcCons --no-headers | Select-String "gw-provider1" | Select-Object -First 1
if (-not $gwLine) {
    Write-Info "Gateway gw-provider1 non trovato. Salto traffic check."
    exit 0
}
$gwPod = (($gwLine.ToString()) -split "\s+")[0]

# Phase A: direct=false, expect packets on consumer gateway.
kubectl annotate svc demo-server -n hub-spoke-demo --kubeconfig $kcCons use-direct-connections=false --overwrite | Out-Null
Start-Sleep -Seconds 3
$jobA = Start-Job -ScriptBlock {
    param($pod, $kc)
    kubectl exec -n liqo-tenant-provider1 $pod --kubeconfig $kc -- tcpdump -i any -n port 80 -c 4
} -ArgumentList $gwPod, $kcCons
Start-Sleep -Seconds 2
kubectl exec -n hub-spoke-demo client-on-provider2 -c netshoot --kubeconfig $kcCons -- sh -c "curl -s -o /dev/null -w '%{http_code}' http://demo-server.hub-spoke-demo.svc.cluster.local" | Out-Host
Wait-Job $jobA -Timeout 20 | Out-Null
$stateA = (Get-Job -Id $jobA.Id).State
if ($stateA -eq "Running") {
    Stop-Job $jobA | Out-Null
    Write-Info "Phase A: tcpdump timeout (unexpected for hub-spoke)"
}
$dumpA = Receive-Job $jobA -ErrorAction SilentlyContinue
Remove-Job $jobA -Force
if ($dumpA) {
    Write-Ok "Phase A: traffico visibile su consumer gateway (hub-spoke)"
} else {
    Write-Info "Phase A: nessun pacchetto catturato"
}

# Phase B: direct=true, expect no packets on consumer gateway.
kubectl annotate svc demo-server -n hub-spoke-demo --kubeconfig $kcCons use-direct-connections=true --overwrite | Out-Null
Start-Sleep -Seconds 5
$jobB = Start-Job -ScriptBlock {
    param($pod, $kc)
    kubectl exec -n liqo-tenant-provider1 $pod --kubeconfig $kc -- tcpdump -i any -n port 80 -c 2
} -ArgumentList $gwPod, $kcCons
Start-Sleep -Seconds 2
kubectl exec -n hub-spoke-demo client-on-provider2 -c netshoot --kubeconfig $kcCons -- sh -c "curl -s -o /dev/null -w '%{http_code}' http://demo-server.hub-spoke-demo.svc.cluster.local" | Out-Host
Wait-Job $jobB -Timeout 14 | Out-Null
$stateB = (Get-Job -Id $jobB.Id).State
if ($stateB -eq "Running") {
    Stop-Job $jobB | Out-Null
    Write-Ok "Phase B: nessun pacchetto su consumer gateway (direct path)"
} else {
    Write-Info "Phase B: pacchetti rilevati su consumer gateway"
}
$dumpB = Receive-Job $jobB -ErrorAction SilentlyContinue
Remove-Job $jobB -Force
if ($dumpB) {
    Write-Host $dumpB
}

Write-Ok "Post-setup direct-path routine completata"
