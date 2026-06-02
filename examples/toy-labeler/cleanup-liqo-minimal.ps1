# ============================================================
# cleanup-liqo-minimal.ps1 – Rimuove SOLO i 3 cluster
#   consumer, provider1 e provider2. Gli altri cluster
#   eventualmente presenti non vengono toccati.
# ============================================================
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Definition

$CONSUMER  = "consumer"
$PROVIDER1 = "provider1"
$PROVIDER2 = "provider2"

$KC_CONSUMER  = Join-Path $here "liqo_kubeconf_consumer"
$KC_PROVIDER1 = Join-Path $here "liqo_kubeconf_provider1"
$KC_PROVIDER2 = Join-Path $here "liqo_kubeconf_provider2"

function Write-Info    { param($msg) Write-Host "[INFO]    $msg" -ForegroundColor Blue }
function Write-Success { param($msg) Write-Host "[SUCCESS] $msg" -ForegroundColor Green }

Write-Info "=== Cleanup: eliminazione cluster consumer, provider1, provider2 ==="

foreach ($cluster in @($CONSUMER, $PROVIDER1, $PROVIDER2)) {
    Write-Info "Eliminazione cluster '$cluster'..."
    $ErrorActionPreference = "SilentlyContinue"
    kind delete cluster --name $cluster 2>&1 | Out-Null
    $ErrorActionPreference = "Stop"
    Write-Success "Cluster '$cluster' eliminato."
}

# Rimuove i kubeconfig generati
foreach ($kc in @($KC_CONSUMER, $KC_PROVIDER1, $KC_PROVIDER2)) {
    if (Test-Path $kc) { Remove-Item $kc -Force }
}

Write-Success "Cleanup completato. Gli altri cluster non sono stati toccati."
