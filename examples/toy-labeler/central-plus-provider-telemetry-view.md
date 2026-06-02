# Vista Cluster Centrale + Interrogazione Provider (Telemetria)

## Perche questa procedura e necessaria

Nel tuo scenario Liqo, il cluster centrale (consumer) vede bene le proprie connessioni verso i provider, ma non ha visibilita completa del link diretto tra provider1 e provider2.

In pratica:
- dal consumer leggi la telemetria dei link `consumer->provider1` e `consumer->provider2`
- per leggere la telemetria del link `provider1<->provider2` devi interrogare i provider

Quindi, per una decisione `Auto` basata su telemetria, devi combinare:
1. vista centrale (consumer)
2. vista remota provider1/provider2

Questa guida contiene i comandi esatti per farlo.

---

## Prerequisiti

- Ambiente gia avviato con gli script toy-labeler
- Kubeconfig presenti in `examples/toy-labeler/`

File attesi:
- `examples/toy-labeler/liqo_kubeconf_consumer`
- `examples/toy-labeler/liqo_kubeconf_provider1`
- `examples/toy-labeler/liqo_kubeconf_provider2`

---

## 1) Inizializza variabili (PowerShell)

```powershell
$KC_CONSUMER  = "examples/toy-labeler/liqo_kubeconf_consumer"
$KC_PROVIDER1 = "examples/toy-labeler/liqo_kubeconf_provider1"
$KC_PROVIDER2 = "examples/toy-labeler/liqo_kubeconf_provider2"
```

Verifica rapida kubeconfig:

```powershell
Test-Path $KC_CONSUMER
Test-Path $KC_PROVIDER1
Test-Path $KC_PROVIDER2
```

---

## 2) Vista dal cluster centrale (consumer)

Elenco connessioni con stato e latenza:

```powershell
kubectl --kubeconfig $KC_CONSUMER get connections.networking.liqo.io -A
```

Output compatto:

```powershell
kubectl --kubeconfig $KC_CONSUMER get connections.networking.liqo.io -A -o jsonpath="{range .items[*]}{.metadata.namespace}{'/'}{.metadata.name}{' type='}{.spec.type}{' status='}{.status.value}{' latency='}{.status.latency.value}{' ts='}{.status.latency.timestamp}{'\n'}{end}"
```

Interpretazione:
- qui vedi i link dal consumer verso provider1/provider2
- non e garantito che tu veda il valore del diretto provider1<->provider2

---

## 3) Interroga i provider per il link diretto p1<->p2

### 3.1 Valore diretto visto da provider1 verso provider2

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -n liqo-tenant-provider2 gw-provider2 -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"
```

### 3.2 Valore diretto visto da provider2 verso provider1

```powershell
kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -n liqo-tenant-provider1 gw-provider1 -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"
```

Nota:
- avere entrambe le direzioni e utile, perche i valori possono non essere identici

---

## 4) Sanity check dei link via consumer (lato provider)

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -n liqo-tenant-consumer gw-consumer -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"

kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -n liqo-tenant-consumer gw-consumer -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"
```

Questo serve per confrontare qualitativamente:
- percorso diretto provider-provider
- percorso via consumer

---

## 5) Simulazione completa in un solo blocco

Copia e incolla questo blocco PowerShell:

```powershell
$KC_CONSUMER  = "examples/toy-labeler/liqo_kubeconf_consumer"
$KC_PROVIDER1 = "examples/toy-labeler/liqo_kubeconf_provider1"
$KC_PROVIDER2 = "examples/toy-labeler/liqo_kubeconf_provider2"

Write-Output "=== CENTRAL VIEW (consumer) ==="
kubectl --kubeconfig $KC_CONSUMER get connections.networking.liqo.io -A -o jsonpath="{range .items[*]}{.metadata.namespace}{'/'}{.metadata.name}{' type='}{.spec.type}{' status='}{.status.value}{' latency='}{.status.latency.value}{' ts='}{.status.latency.timestamp}{'\n'}{end}"

Write-Output "=== PROVIDER1 direct link to provider2 ==="
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -n liqo-tenant-provider2 gw-provider2 -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"

Write-Output "=== PROVIDER2 direct link to provider1 ==="
kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -n liqo-tenant-provider1 gw-provider1 -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"

Write-Output "=== provider1 <-> consumer ==="
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -n liqo-tenant-consumer gw-consumer -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"

Write-Output "=== provider2 <-> consumer ==="
kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -n liqo-tenant-consumer gw-consumer -o jsonpath="name={.metadata.name} type={.spec.type} status={.status.value} latency={.status.latency.value} ts={.status.latency.timestamp}{'\n'}"
```

---

## 6) Calcolo rapido media latenza diretta (opzionale)

Questo blocco converte automaticamente `ms` in microsecondi e calcola la media semplice del diretto:

```powershell
$KC_PROVIDER1 = "examples/toy-labeler/liqo_kubeconf_provider1"
$KC_PROVIDER2 = "examples/toy-labeler/liqo_kubeconf_provider2"

function To-Microseconds([string]$v) {
  $n = [double](([regex]::Match($v, '[0-9.]+')).Value)
  if ($v -like '*ms') { return $n * 1000 }
  return $n
}

$d12 = kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -n liqo-tenant-provider2 gw-provider2 -o jsonpath="{.status.latency.value}"
$d21 = kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -n liqo-tenant-provider1 gw-provider1 -o jsonpath="{.status.latency.value}"

$avg = ((To-Microseconds $d12) + (To-Microseconds $d21)) / 2

Write-Output "direct_p1_to_p2=$d12"
Write-Output "direct_p2_to_p1=$d21"
Write-Output ("direct_avg_simple={0:N1}us ({1:N3}ms)" -f $avg, ($avg/1000))
```

---

## 7) Regola minima per usare questi dati in Auto

Prima di usare le latenze in decisione automatica:

1. Tutte le `Connection` coinvolte devono essere `Connected`
2. `status.latency.value` non deve essere `N/A`
3. Timestamp recente (es. aggiornato negli ultimi 30-60 secondi)

Se una di queste condizioni fallisce:
- non cambiare path
- mantieni fallback conservativo

---

## 8) Troubleshooting veloce

### Caso A: `Error` o `latency=N/A`

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get events -n liqo-tenant-provider2 --sort-by=.lastTimestamp | Select-Object -Last 20
kubectl --kubeconfig $KC_PROVIDER2 get events -n liqo-tenant-provider1 --sort-by=.lastTimestamp | Select-Object -Last 20
```

### Caso B: vuoi reimpostare il p2p provider-provider

```powershell
liqoctl network disconnect --kubeconfig $KC_PROVIDER1 --remote-kubeconfig $KC_PROVIDER2
liqoctl network connect --kubeconfig $KC_PROVIDER1 --remote-kubeconfig $KC_PROVIDER2 --gw-server-service-type NodePort
```

### Caso C: controlla subito lo stato finale

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -A
kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -A
```

---

## Conclusione

Dal solo consumer non hai la telemetria completa del diretto provider1<->provider2.

Per una decisione `Auto` robusta, devi sempre combinare:
- vista del cluster centrale
- interrogazione telemetrica dei provider
