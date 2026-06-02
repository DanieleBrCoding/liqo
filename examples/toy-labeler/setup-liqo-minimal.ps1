# ============================================================
# setup-liqo-minimal.ps1 – Crea 3 cluster kind (1 consumer +
#   2 provider), installa Liqo e fa il peering.
#   Builda le immagini Liqo dal branch locale (virtual-kubelet
#   e liqo-controller-manager) per includere il codice custom.
# ============================================================
param(
    [switch]$EnableDirectProviderTunnel
)

$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Definition
$MANIFESTS = Join-Path $here "manifests"
$ROOT = Resolve-Path (Join-Path $here "..\..")

# ---------- versione Liqo da installare ----------
$LIQO_VERSION = "v1.0.1"

# ---------- tag locale univoco per forzare uso build del branch ----------
$LOCAL_IMAGE_TAG = "$LIQO_VERSION-local-$(Get-Date -Format 'yyyyMMddHHmmss')"

# ---------- componenti da buildare dal branch locale ----------
$LOCAL_COMPONENTS = @("virtual-kubelet", "liqo-controller-manager")

# ---------- nomi e kubeconfig ----------
$CONSUMER  = "consumer"
$PROVIDER1 = "provider1"
$PROVIDER2 = "provider2"

$KC_CONSUMER  = Join-Path $here "liqo_kubeconf_consumer"
$KC_PROVIDER1 = Join-Path $here "liqo_kubeconf_provider1"
$KC_PROVIDER2 = Join-Path $here "liqo_kubeconf_provider2"

# ---------- helper ----------
function Write-Info    { param($msg) Write-Host "[INFO]    $msg" -ForegroundColor Blue }
function Write-Success { param($msg) Write-Host "[SUCCESS] $msg" -ForegroundColor Green }
function Write-Err     { param($msg) Write-Host "[ERROR]   $msg" -ForegroundColor Red }

# ---------- prerequisiti ----------
foreach ($cmd in @("docker", "kubectl", "kind", "liqoctl", "go")) {
    if (-not (Get-Command $cmd -ErrorAction SilentlyContinue)) {
        Write-Err "$cmd non trovato. Installalo per continuare."
        exit 1
    }
}

# ============================================================
# 1. Creazione dei 3 cluster (cancella SOLO consumer/provider1/provider2)
# ============================================================
Write-Info "=== STEP 1: Creazione dei 3 cluster kind ==="

$clusters = @(
    @{ Name = $CONSUMER;  KC = $KC_CONSUMER;  Config = "cluster-consumer.yaml"  },
    @{ Name = $PROVIDER1; KC = $KC_PROVIDER1; Config = "cluster-provider1.yaml" },
    @{ Name = $PROVIDER2; KC = $KC_PROVIDER2; Config = "cluster-provider2.yaml" }
)

foreach ($c in $clusters) {
    Write-Info "Eliminazione eventuale cluster '$($c.Name)'..."
    $ErrorActionPreference = "SilentlyContinue"
    kind delete cluster --name $c.Name 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"

    $cfg = Join-Path $MANIFESTS $c.Config
    Write-Info "Creazione cluster '$($c.Name)'..."
    kind create cluster --name $c.Name --kubeconfig $c.KC --config $cfg --wait 5m
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore nella creazione del cluster '$($c.Name)'."; exit 1 }
    Write-Success "Cluster '$($c.Name)' creato."
}

# ============================================================
# 2. Installazione Prometheus Operator CRD (richieste per metrics)
# ============================================================
Write-Info "=== STEP 2: Installazione Prometheus Operator CRD ==="

foreach ($c in $clusters) {
    Write-Info "Installazione Prometheus Operator CRD su '$($c.Name)'..."
    kubectl apply -f https://github.com/prometheus-operator/prometheus-operator/releases/download/v0.70.0/stripped-down-crds.yaml --kubeconfig $c.KC
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore installazione Prometheus CRD su '$($c.Name)'."; exit 1 }
    Write-Success "Prometheus CRD installato su '$($c.Name)'."
}

# ============================================================
# 3. Installazione Liqo sui 3 cluster
# ============================================================
Write-Info "=== STEP 3: Installazione Liqo ==="

foreach ($c in $clusters) {
    Write-Info "Installazione Liqo su '$($c.Name)'..."
    liqoctl install kind --cluster-id $c.Name `
        --cluster-labels="cl.liqo.io/name=$($c.Name)" `
        --kubeconfig $c.KC `
        --version $LIQO_VERSION `
        --enable-metrics
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore installazione Liqo su '$($c.Name)'."; exit 1 }
    Write-Success "Liqo installato su '$($c.Name)'."
}

# ============================================================
# 4. Peering: consumer <-> provider1, consumer <-> provider2
# ============================================================
Write-Info "=== STEP 4: Peering ==="

$env:KUBECONFIG = $KC_CONSUMER

Write-Info "Peering consumer <-> provider1..."
liqoctl peer --remote-kubeconfig $KC_PROVIDER1 --gw-server-service-type NodePort
if ($LASTEXITCODE -ne 0) { Write-Err "Errore peering consumer <-> provider1."; exit 1 }
Write-Success "Peering consumer <-> provider1 completato."

Write-Info "Peering consumer <-> provider2..."
liqoctl peer --remote-kubeconfig $KC_PROVIDER2 --gw-server-service-type NodePort
if ($LASTEXITCODE -ne 0) { Write-Err "Errore peering consumer <-> provider2."; exit 1 }
Write-Success "Peering consumer <-> provider2 completato."

Remove-Item Env:\KUBECONFIG

# ============================================================
# 5. Build immagini Liqo dal branch locale e caricamento in kind
# ============================================================
Write-Info "=== STEP 5: Build immagini locali dal branch ==="

Push-Location $ROOT

foreach ($comp in $LOCAL_COMPONENTS) {
    $image = "ghcr.io/liqotech/${comp}:${LOCAL_IMAGE_TAG}"

    Write-Info "Compilazione $comp per linux/amd64..."
    $env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
    New-Item -ItemType Directory -Force -Path "bin\amd64" | Out-Null
    go build -ldflags="-s -w" -o "bin/amd64/${comp}_linux_amd64" "./cmd/$comp"
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore compilazione $comp."; Pop-Location; exit 1 }
    Remove-Item Env:\GOOS; Remove-Item Env:\GOARCH; Remove-Item Env:\CGO_ENABLED

    Write-Info "Build immagine Docker $image..."
    docker build --build-arg COMPONENT=$comp -t $image -f build/liqo/Dockerfile .
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore build immagine $comp."; Pop-Location; exit 1 }
    Write-Success "Immagine $image buildata."

    foreach ($c in $clusters) {
        Write-Info "Caricamento $image in '$($c.Name)'..."
        $ErrorActionPreference = "SilentlyContinue"
        kind load docker-image $image --name $c.Name 2>&1 | Out-Null
        $ErrorActionPreference = "Stop"
    }
    Write-Success "$comp caricato in tutti i cluster."
}

Pop-Location

# ============================================================
# 6. Restart pod per usare le immagini locali
# ============================================================
Write-Info "=== STEP 6: Restart pod Liqo ==="

foreach ($c in $clusters) {
    Write-Info "Restart liqo-controller-manager su '$($c.Name)'..."
    $ErrorActionPreference = "SilentlyContinue"
    kubectl rollout restart deployment liqo-controller-manager -n liqo --kubeconfig $c.KC 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
}

# Restart virtual-kubelet sul consumer (gestiscono reflection EndpointSlice)
Write-Info "Restart virtual-kubelet sul consumer..."
$ErrorActionPreference = "SilentlyContinue"
kubectl get deployment -n liqo-tenant-provider1 --kubeconfig $KC_CONSUMER -o name 2>&1 | ForEach-Object {
    kubectl rollout restart $_ -n liqo-tenant-provider1 --kubeconfig $KC_CONSUMER 2>&1 | Out-Null
}
kubectl get deployment -n liqo-tenant-provider2 --kubeconfig $KC_CONSUMER -o name 2>&1 | ForEach-Object {
    kubectl rollout restart $_ -n liqo-tenant-provider2 --kubeconfig $KC_CONSUMER 2>&1 | Out-Null
}
$ErrorActionPreference = "Stop"

Write-Info "Attendo che i pod siano pronti (30s)..."
Start-Sleep -Seconds 30

Write-Success "Pod riavviati con immagini locali."

# ============================================================
# 6. Forza i deployment Liqo a usare il tag locale univoco
# ============================================================
Write-Info "=== STEP 6: Patch deployment image -> tag locale univoco ==="

foreach ($c in $clusters) {
    $controllerImage = "ghcr.io/liqotech/liqo-controller-manager:${LOCAL_IMAGE_TAG}"
    Write-Info "Patch liqo-controller-manager su '$($c.Name)' -> $controllerImage"
    kubectl set image deployment/liqo-controller-manager `
        controller-manager=$controllerImage `
        -n liqo --kubeconfig $c.KC
    if ($LASTEXITCODE -ne 0) { Write-Err "Patch image controller-manager fallita su '$($c.Name)'"; exit 1 }
}

$vkImage = "ghcr.io/liqotech/virtual-kubelet:${LOCAL_IMAGE_TAG}"

$vkP1Deploy = kubectl get deployment -n liqo-tenant-provider1 --kubeconfig $KC_CONSUMER -o name | Select-String "vk-provider1" | Select-Object -First 1
if ($vkP1Deploy) {
    $vkP1Name = $vkP1Deploy.ToString().Trim()
    Write-Info "Patch $vkP1Name in liqo-tenant-provider1 -> $vkImage"
    kubectl set image $vkP1Name virtual-kubelet=$vkImage -n liqo-tenant-provider1 --kubeconfig $KC_CONSUMER
    if ($LASTEXITCODE -ne 0) { Write-Err "Patch image virtual-kubelet provider1 fallita"; exit 1 }
}

$vkP2Deploy = kubectl get deployment -n liqo-tenant-provider2 --kubeconfig $KC_CONSUMER -o name | Select-String "vk-provider2" | Select-Object -First 1
if ($vkP2Deploy) {
    $vkP2Name = $vkP2Deploy.ToString().Trim()
    Write-Info "Patch $vkP2Name in liqo-tenant-provider2 -> $vkImage"
    kubectl set image $vkP2Name virtual-kubelet=$vkImage -n liqo-tenant-provider2 --kubeconfig $KC_CONSUMER
    if ($LASTEXITCODE -ne 0) { Write-Err "Patch image virtual-kubelet provider2 fallita"; exit 1 }
}

Write-Info "Attendo rollout deployment patchati..."
foreach ($c in $clusters) {
    kubectl rollout status deployment/liqo-controller-manager -n liqo --kubeconfig $c.KC --timeout=180s | Out-Null
}
if ($vkP1Deploy) {
    kubectl rollout status $vkP1Name -n liqo-tenant-provider1 --kubeconfig $KC_CONSUMER --timeout=180s | Out-Null
}
if ($vkP2Deploy) {
    kubectl rollout status $vkP2Name -n liqo-tenant-provider2 --kubeconfig $KC_CONSUMER --timeout=180s | Out-Null
}
Write-Success "Deployment patchati con tag locale univoco."

# ============================================================
# 7. Opzionale: connessione diretta provider1 <-> provider2
# ============================================================
if ($EnableDirectProviderTunnel) {
    Write-Info "=== STEP 7: Connessione diretta provider1 <-> provider2 ==="
    liqoctl network connect --kubeconfig $KC_PROVIDER1 --remote-kubeconfig $KC_PROVIDER2 --gw-server-service-type NodePort
    if ($LASTEXITCODE -ne 0) { Write-Err "Errore creazione connessione diretta provider1 <-> provider2"; exit 1 }
    Write-Success "Connessione diretta provider1 <-> provider2 creata."
}

# ============================================================
# Done
# ============================================================
Write-Host ""
Write-Success "=== 3 cluster con Liqo pronti (immagini dal branch locale)! ==="
Write-Host ""
Write-Info "Tag locale usato per le immagini patchate:"
Write-Host "  $LOCAL_IMAGE_TAG"
Write-Host ""
Write-Info "Kubeconfig:"
Write-Host "  Consumer:  $KC_CONSUMER"
Write-Host "  Provider1: $KC_PROVIDER1"
Write-Host "  Provider2: $KC_PROVIDER2"
Write-Host ""
Write-Info "Per lavorare su un cluster:"
Write-Host "  kind export kubeconfig --name consumer"
Write-Host "  kind export kubeconfig --name provider1"
Write-Host "  kind export kubeconfig --name provider2"
Write-Host ""
Write-Info "Cleanup (solo questi 3 cluster):"
Write-Host "  $here\cleanup-liqo-minimal.ps1"
