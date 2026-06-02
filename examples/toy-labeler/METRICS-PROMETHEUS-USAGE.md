# Liqo Prometheus Metrics - Utilizzo per Auto Telemetry

## Configurazione Abilitata ✅

Le metriche Liqo sono state **abilitate** nel setup con il flag `--enable-metrics`.

**Componenti installati:**
- Prometheus Operator CRD (`podmonitors.monitoring.coreos.com`, `servicemonitors.monitoring.coreos.com`)
- Liqo Metric Agent che espone le metriche su porta 8443
- PodMonitor e ServiceMonitor automaticamente creati da Liqo

## Metrica Disponibile: `liqo_peer_latency_us`

**Descrizione:**
- RTT (Round-Trip Time) latency tra cluster locali e remoti
- Misurata via UDP `ping` periodico tra i gateway Liqo
- Valore in **microsecondi (μs)**

**Etichette (labels):**
- `src_cluster_id`: cluster sorgente
- `dst_cluster_id`: cluster destinazione
- `remote_cluster_name`: nome del cluster remoto

## Accesso alle Metriche dal Consumer

### Metodo 1: Interrogare Direttamente il Metric Agent (Port-Forward)

```powershell
$CONSUMER_KC = "C:\Users\Polito\Desktop\POLITO\tesi_studio\progetto_tesi\liqo\examples\toy-labeler\liqo_kubeconf_consumer"

# Avviare port-forward in background
kubectl port-forward -n liqo svc/liqo-metric-agent 9090:8443 --kubeconfig=$CONSUMER_KC | Out-Null &

# Attendere che il port-forward si stabilizzi
Start-Sleep -Seconds 2

# Interrogare le metriche (con TLS e certificato autofirmato)
$ProgressPreference = 'SilentlyContinue'
[System.Net.ServicePointManager]::ServerCertificateValidationCallback = {$true}
$response = Invoke-WebRequest -Uri "https://localhost:9090/metrics" -UseBasicParsing -ErrorAction SilentlyContinue

# Estrarre la latenza verso provider1
$response.Content | Select-String "liqo_peer_latency_us.*provider1"
```

### Metodo 2: Usare il Kubeconfig Memorizzato per Interrogare il Provider (Avanzato)

Se il kubeconfig interno non è raggiungibile, il consumer può:

1. **Estrarre il kubeconfig dal Secret** del provider dal proprio namespace tenant
2. **Usare un pod nel consumer** come proxy per interrogare il provider localmente

```powershell
# Nel provider1:
$PROVIDER1_KC = "C:\Users\Polito\Desktop\POLITO\tesi_studio\progetto_tesi\liqo\examples\toy-labeler\liqo_kubeconf_provider1"

# Latenza verso provider2
kubectl get connection gw-provider2 -n liqo-tenant-provider2 -o jsonpath='{.status.latency.value}' --kubeconfig=$PROVIDER1_KC
```

## Metriche Esposte da Liqo

Oltre a `liqo_peer_latency_us`, sono disponibili:

| Metrica | Descrizione |
|---------|-------------|
| `liqo_peer_latency_us` | RTT latency in microsecond (μs) |
| `liqo_peer_receive_bytes_total` | Byte ricevuti da cluster remoto |
| `liqo_peer_transmit_bytes_total` | Byte trasmessi verso cluster remoto |
| `liqo_peer_is_connected` | Boolean: peering attivo (1=sì, 0=no) |
| `liqo_virtual_kubelet_reflection_item_counter` | Risorse riflesse (Pod, ConfigMap, Secret, ecc.) |

## Integrazione con Auto Telemetry Solution

Per l'Auto Telemetry engine, il consumer può:

1. **Recuperare le metriche periodicamente** dal metric-agent via port-forward
2. **Aggregare i dati** per calcolare il score di routing:
   ```
   SCORE = 0.6*RTT_norm + 0.3*LOSS_norm + 0.1*TIMEOUT_norm
   ```
3. **Memorizzare lo score** in ConfigMap/etcd per decision-making
4. **Interrogare il provider** per latenze dirette provider-to-provider via kubeconfig memorizzato

## Verifica Immediata

```powershell
# Latenza consumer↔provider1
$CONSUMER_KC = "..."
kubectl get connection gw-provider1 -n liqo-tenant-provider1 -o jsonpath='{.status.latency.value}' --kubeconfig=$CONSUMER_KC

# Latenza provider1↔provider2
$PROVIDER1_KC = "..."
kubectl get connection gw-provider2 -n liqo-tenant-provider2 -o jsonpath='{.status.latency.value}' --kubeconfig=$PROVIDER1_KC
```

## Documentazione Ufficiale

Vedi: https://docs.liqo.io/en/v1.1.2/usage/prometheus-metrics.html
