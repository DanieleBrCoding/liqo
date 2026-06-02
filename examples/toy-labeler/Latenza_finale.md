# Analisi della Latenza: Routing Diretto vs Indiretto tramite EndpointSlice

In Liqo, il modo più efficace e accurato per testare la latenza reale e osservare il cambio di instradamento del traffico (da Indiretto a Diretto) è bypassare il ClusterIP del Service e puntare esplicitamente all'indirizzo contenuto nell'**EndpointSlice**.

## Perché interrogare l'EndpointSlice?

- **Service IP (ClusterIP):** È statico e maschera il funzionamento interno. Se lo usiamo, `kube-proxy` e Liqo gestiscono il traffico sotto il cofano, e noi non abbiamo evidenza visiva del reale percorso del pacchetto.
- **EndpointSlice IP:** È un campo **dinamico**, gestito e aggiornato costantemente dal **Virtual Kubelet** di Liqo in base alla topologia di rete attiva in quel momento.

## I Due Scenari a Confronto

Tracciando l'IP nell'EndpointSlice dal punto di vista del **Provider 2** (dove risiede il client), possiamo evidenziare due comportamenti netti:

### 1. Connessione Indiretta (Hub-and-Spoke / Default)
Fin tanto che il traffico passa tramite il cluster centrale:
- L'indirizzo riflesso all'interno dell'EndpointSlice è un **indirizzo NATtato** (es. `10.6x.x.x`) generato da Liqo sul gateway del *Consumer*.
- **Flusso fisico:** Provider 2 ➔ Consumer (Hub) ➔ Provider 1.
- **Latenza:** Maggiore, a causa del routing a "V" (hairpinning) obbligato attraverso il nodo centrale.

### 2. Connessione Diretta (P2P Path)
Quando il *toy-labeler* interviene annotando il service con `use-direct-connections=true`:
- Il Virtual Kubelet del Provider *riscrive in tempo reale* l'EndpointSlice, sostituendo l'IP NATtato con il vero IP di recapito associato al tunnel diretto (Provider 1).
- **Flusso fisico:** Provider 2 ➔ Provider 1 (bypassando il Consumer a livello di piano dati).
- **Latenza:** Minore. L'instradamento è ottimizzato e i due cluster periferici comunicano direttamente.

## Comandi per il Test (PowerShell)

Dato che sei su Windows, puoi utilizzare questo script PowerShell per prelevare dinamicamente l'IP dall'EndpointSlice e avviare il test di latenza tramite `ping`. L'utilizzo di variabili PowerShell rende il comando più pulito rispetto alla sub-shell bash `$()`.

```powershell
kubectl --kubeconfig liqo_kubeconf_provider1 -n hub-spoke-demo exec client-on-provider1 -c netshoot -- ping $(kubectl --kubeconfig liqo_kubeconf_provider1 -n hub-spoke-demo get endpointslice -l kubernetes.io/service-name=demo-server -o jsonpath="{.items[0].endpoints[0].addresses[0]}")
```

> **Come effettuare il test completo:**
> Esegui questa sequenza una prima volta. Otterrai i tempi di latenza attraversando il Consumer.
> Lascia agire il `toy-labeler` in modo che sposti il traffico, e poi riesegui esattamente gli stessi comandi: vedrai che `$ENDPOINT_IP` cambierà radicalmente e i millisecondi (ms) del ping rifletteranno la latenza inferiore del percorso Peer-to-Peer.




###  Estrapolare la latenza della connessione diretta
PEr la connessione diretta essendo vicini si può usare questo comando:
```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_provider1 get connections.networking.liqo.io -A -l liqo.io/remote-cluster-id=provider2 -o jsonpath="{.items[*].status.latency.value}"