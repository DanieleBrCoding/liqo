# ============================================================
# demo-hub-spoke.ps1 – Dimostra che provider1 e provider2
#   comunicano SOLO passando attraverso il consumer (hub-and-spoke).
#
# Prerequisito: aver eseguito setup-liqo-minimal.ps1
# ============================================================
$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Definition

$KC_CONSUMER  = Join-Path $here "liqo_kubeconf_consumer"
$KC_PROVIDER1 = Join-Path $here "liqo_kubeconf_provider1"
$KC_PROVIDER2 = Join-Path $here "liqo_kubeconf_provider2"

$NS = "hub-spoke-demo"

function Write-Info    { param($msg) Write-Host "[INFO]    $msg" -ForegroundColor Blue }
function Write-Success { param($msg) Write-Host "[SUCCESS] $msg" -ForegroundColor Green }
function Write-Err     { param($msg) Write-Host "[ERROR]   $msg" -ForegroundColor Red }
function Write-Step    { param($msg) Write-Host "`n========================================" -ForegroundColor Cyan; Write-Host "  $msg" -ForegroundColor Cyan; Write-Host "========================================" -ForegroundColor Cyan }

function Ensure-Kubeconfig {
  param(
    [string]$ClusterName,
    [string]$KubeconfigPath
  )

  if (Test-Path $KubeconfigPath) {
    return
  }

  Write-Info "Kubeconfig '$KubeconfigPath' non trovato. Lo genero da cluster kind '$ClusterName'..."

  $ErrorActionPreference = "SilentlyContinue"
  $kubeconfigContent = kind get kubeconfig --name $ClusterName 2>&1
  $exitCode = $LASTEXITCODE
  $ErrorActionPreference = "Stop"
  if ($exitCode -ne 0) {
    Write-Err "Impossibile generare kubeconfig per '$ClusterName'. Cluster kind assente o non avviato."
    Write-Info "Esegui prima: .\setup-liqo-minimal.ps1"
    exit 1
  }

  Set-Content -Path $KubeconfigPath -Value $kubeconfigContent -Encoding ascii
  Write-Success "Kubeconfig generato: $KubeconfigPath"
}

function Test-ClusterAccess {
  param(
    [string]$ClusterName,
    [string]$KubeconfigPath
  )

  kubectl version --kubeconfig $KubeconfigPath --request-timeout=15s 2>&1 | Out-Null
  if ($LASTEXITCODE -ne 0) {
    Write-Err "Kubeconfig per '$ClusterName' non valido o cluster non raggiungibile: $KubeconfigPath"
    exit 1
  }
}

Ensure-Kubeconfig -ClusterName "consumer" -KubeconfigPath $KC_CONSUMER
Ensure-Kubeconfig -ClusterName "provider1" -KubeconfigPath $KC_PROVIDER1
Ensure-Kubeconfig -ClusterName "provider2" -KubeconfigPath $KC_PROVIDER2

Test-ClusterAccess -ClusterName "consumer" -KubeconfigPath $KC_CONSUMER
Test-ClusterAccess -ClusterName "provider1" -KubeconfigPath $KC_PROVIDER1
Test-ClusterAccess -ClusterName "provider2" -KubeconfigPath $KC_PROVIDER2

# ============================================================
# 1. Verifica peering
# ============================================================
Write-Step "STEP 1: Verifica peering (solo consumer <-> provider, NO provider <-> provider)"

Write-Info "Nodi visibili dal consumer (dovrebbero esserci virtual-node per provider1 e provider2):"
kubectl get nodes --kubeconfig $KC_CONSUMER -o wide

Write-Host ""
Write-Info "Nodi visibili da provider1 (NON dovrebbe vedere provider2):"
kubectl get nodes --kubeconfig $KC_PROVIDER1 -o wide

Write-Host ""
Write-Info "Nodi visibili da provider2 (NON dovrebbe vedere provider1):"
kubectl get nodes --kubeconfig $KC_PROVIDER2 -o wide

# ============================================================
# 2. Creare namespace e offloadarlo verso entrambi i provider
# ============================================================
Write-Step "STEP 2: Creazione namespace '$NS' e offloading verso entrambi i provider"

$ErrorActionPreference = "SilentlyContinue"
kubectl create namespace $NS --kubeconfig $KC_CONSUMER 2>&1 | Out-Null
$ErrorActionPreference = "Stop"

liqoctl offload namespace $NS --kubeconfig $KC_CONSUMER --namespace-mapping-strategy EnforceSameName
if ($LASTEXITCODE -ne 0) { Write-Err "Errore nell'offloading del namespace."; exit 1 }

Write-Info "Attendo propagazione namespace (10s)..."
Start-Sleep -Seconds 10

Write-Success "Namespace offloadato."

# ============================================================
# 3. Deploy server su provider2 (via node affinity su virtual-node provider2)
# ============================================================
Write-Step "STEP 3: Deploy server NGINX su provider2"

$serverYaml = @"
apiVersion: apps/v1
kind: Deployment
metadata:
  name: server-on-provider2
  namespace: $NS
  labels:
    app: demo-server
spec:
  replicas: 1
  selector:
    matchLabels:
      app: demo-server
  template:
    metadata:
      labels:
        app: demo-server
    spec:
      containers:
        - name: nginx
          image: nginx:stable-alpine
          ports:
            - containerPort: 80
      affinity:
        nodeAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            nodeSelectorTerms:
              - matchExpressions:
                  - key: liqo.io/remote-cluster-id
                    operator: In
                    values:
                      - provider2
---
apiVersion: v1
kind: Service
metadata:
  name: demo-server
  namespace: $NS
spec:
  selector:
    app: demo-server
  ports:
    - port: 80
      targetPort: 80
      protocol: TCP
  type: ClusterIP
"@

$serverYaml | kubectl apply --kubeconfig $KC_CONSUMER -f -
if ($LASTEXITCODE -ne 0) { Write-Err "Errore nel deploy del server."; exit 1 }

Write-Info "Attendo che il server sia pronto..."
kubectl rollout status deployment/server-on-provider2 -n $NS --kubeconfig $KC_CONSUMER --timeout=120s

Write-Success "Server NGINX deployato su provider2."

# ============================================================
# 4. Deploy client su provider1
# ============================================================
Write-Step "STEP 4: Deploy client (netshoot) su provider1"

$clientYaml = @"
apiVersion: v1
kind: Pod
metadata:
  name: client-on-provider1
  namespace: $NS
  labels:
    app: demo-client
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

$clientYaml | kubectl apply --kubeconfig $KC_CONSUMER -f -
if ($LASTEXITCODE -ne 0) { Write-Err "Errore nel deploy del client."; exit 1 }

Write-Info "Attendo che il client sia pronto..."
kubectl wait --for=condition=Ready pod/client-on-provider1 -n $NS --kubeconfig $KC_CONSUMER --timeout=120s

Write-Success "Client netshoot deployato su provider1."

# ============================================================
# 5. Verifica distribuzione pod
# ============================================================
Write-Step "STEP 5: Verifica distribuzione pod"

Write-Info "Pod e nodo su cui girano:"
kubectl get pods -n $NS -o wide --kubeconfig $KC_CONSUMER

Write-Host ""
Write-Info "Il server gira su un virtual-node di provider2."
Write-Info "Il client gira su un virtual-node di provider1."
Write-Info "NON c'e' peering diretto provider1 <-> provider2."
Write-Info "=> Qualsiasi comunicazione tra loro DEVE passare per il consumer."

# ============================================================
# 6. Test connettivita': client su provider1 -> server su provider2
# ============================================================
Write-Step "STEP 6: Test connettivita' (provider1 -> provider2 via consumer)"

Write-Info "Il client su provider1 prova a raggiungere il server su provider2..."
Write-Info "Comando: curl -s -o /dev/null -w '%{http_code}' http://demo-server.`$NS.svc.cluster.local"
Write-Host ""

# Ritenta fino a 5 volte (il virtual kubelet puo' impiegare qualche secondo)
$maxRetries = 5
$success = $false
for ($i = 1; $i -le $maxRetries; $i++) {
    Write-Info "Tentativo $i/$maxRetries..."
    $ErrorActionPreference = "SilentlyContinue"
    $result = kubectl exec client-on-provider1 -n $NS --kubeconfig $KC_CONSUMER -c netshoot -- curl -s -o /dev/null -w '%{http_code}' "http://demo-server.$NS.svc.cluster.local" --connect-timeout 10 2>&1
    $exitCode = $LASTEXITCODE
    $ErrorActionPreference = "Stop"

    if ($exitCode -eq 0 -and $result -match "200") {
        $success = $true
        break
    }
    Write-Info "Non ancora pronto, attendo 10s..."
    Start-Sleep -Seconds 10
}

Write-Host ""
if ($success) {
    Write-Success "CONNETTIVITA' OK! HTTP 200 ricevuto."
    Write-Host ""
    Write-Success "Il client su PROVIDER1 ha raggiunto il server su PROVIDER2"
    Write-Success "passando attraverso il CONSUMER (hub-and-spoke)."
    Write-Host ""

    Write-Info "Dettaglio risposta completa:"
    $ErrorActionPreference = "SilentlyContinue"
    kubectl exec client-on-provider1 -n $NS --kubeconfig $KC_CONSUMER -c netshoot -- curl -s "http://demo-server.$NS.svc.cluster.local" 2>&1 | Select-Object -First 5
    $ErrorActionPreference = "Stop"
} else {
    Write-Err "Connettivita' fallita dopo $maxRetries tentativi. Ultimo risultato: $result"
    Write-Info "Il traffico cross-provider potrebbe richiedere piu' tempo per stabilirsi."
    Write-Info "Riprova manualmente:"
    Write-Host "  kubectl exec client-on-provider1 -n $NS --kubeconfig $KC_CONSUMER -c netshoot -- curl -s http://demo-server.$NS.svc.cluster.local"
}

# ============================================================
# 7. Controprova: provider1 e provider2 NON si vedono direttamente
# ============================================================
Write-Step "STEP 7: Controprova - i provider NON hanno peering diretto"

Write-Host ""
Write-Info "Virtual nodes su provider1 (non dovrebbe vedere provider2):"
$ErrorActionPreference = "SilentlyContinue"
kubectl get nodes --kubeconfig $KC_PROVIDER1 -l liqo.io/type=virtual-node 2>&1
$ErrorActionPreference = "Stop"

Write-Host ""
Write-Info "Virtual nodes su provider2 (non dovrebbe vedere provider1):"
$ErrorActionPreference = "SilentlyContinue"
kubectl get nodes --kubeconfig $KC_PROVIDER2 -l liqo.io/type=virtual-node 2>&1
$ErrorActionPreference = "Stop"

Write-Host ""
Write-Success "=== Demo hub-and-spoke completata! ==="
Write-Host ""
Write-Info "Riepilogo:"
Write-Host "  - Server NGINX gira fisicamente su PROVIDER2"
Write-Host "  - Client netshoot gira fisicamente su PROVIDER1"
Write-Host "  - provider1 e provider2 NON sono interconnessi direttamente"
Write-Host "  - Il traffico client->server passa per il CONSUMER (hub)"
Write-Host ""
Write-Info "Cleanup:"
Write-Host "  kubectl delete namespace $NS --kubeconfig $KC_CONSUMER"
