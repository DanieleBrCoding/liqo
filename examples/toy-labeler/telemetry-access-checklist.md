# Telemetry Access Checklist (Toy-Labeler Auto)

Questa checklist verifica se il controller puo interrogare i provider per leggere telemetrie (es. `Connection` e latenza).

Contesto atteso:
- ambiente demo `consumer`, `provider1`, `provider2`
- kubeconfig locali in `examples/toy-labeler/`

File usati:
- `examples/toy-labeler/liqo_kubeconf_consumer`
- `examples/toy-labeler/liqo_kubeconf_provider1`
- `examples/toy-labeler/liqo_kubeconf_provider2`

---

## 0) Preparazione variabili (PowerShell)

```powershell
$KC_CONSUMER  = "examples/toy-labeler/liqo_kubeconf_consumer"
$KC_PROVIDER1 = "examples/toy-labeler/liqo_kubeconf_provider1"
$KC_PROVIDER2 = "examples/toy-labeler/liqo_kubeconf_provider2"
```

Verifica che i file esistano:

```powershell
Test-Path $KC_CONSUMER
Test-Path $KC_PROVIDER1
Test-Path $KC_PROVIDER2
```

Atteso: `True` per tutti.

---

## 1) Reachability API server provider

Obiettivo: capire se l'endpoint API e raggiungibile.

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get --raw=/healthz
kubectl --kubeconfig $KC_PROVIDER2 get --raw=/healthz
```

Atteso: output `ok`.

Se fallisce:
- `Unable to connect` / timeout: problema di rete o kubeconfig endpoint errato
- `Unauthorized`: endpoint raggiungibile ma credenziali non valide

---

## 2) Verifica autenticazione base

Obiettivo: verificare che l'identita nel kubeconfig sia valida.

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get ns
kubectl --kubeconfig $KC_PROVIDER2 get ns
```

Atteso: elenco namespace.

Se fallisce con:
- `Unauthorized`: problema auth (token/cert/scadenza)

---

## 3) Verifica presenza risorsa telemetrica `Connection`

Obiettivo: verificare che il CRD esista sul provider.

```powershell
kubectl --kubeconfig $KC_PROVIDER1 api-resources | Select-String "connections"
kubectl --kubeconfig $KC_PROVIDER2 api-resources | Select-String "connections"
```

Atteso: riga con `connections` nel group `networking.liqo.io`.

Check alternativo:

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get crd connections.networking.liqo.io
kubectl --kubeconfig $KC_PROVIDER2 get crd connections.networking.liqo.io
```

---

## 4) Verifica RBAC minima per Auto

Obiettivo: verificare permessi read-only necessari.

### 4.1 Permessi list/get/watch su `Connection`

```powershell
kubectl --kubeconfig $KC_PROVIDER1 auth can-i get connections.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER1 auth can-i list connections.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER1 auth can-i watch connections.networking.liqo.io --all-namespaces

kubectl --kubeconfig $KC_PROVIDER2 auth can-i get connections.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER2 auth can-i list connections.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER2 auth can-i watch connections.networking.liqo.io --all-namespaces
```

Atteso: `yes` su tutti.

### 4.2 (Opzionale) Permessi su risorse di supporto

Solo se la tua logica le usa.

```powershell
kubectl --kubeconfig $KC_PROVIDER1 auth can-i list wggatewayclients.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER1 auth can-i list wggatewayservers.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER2 auth can-i list wggatewayclients.networking.liqo.io --all-namespaces
kubectl --kubeconfig $KC_PROVIDER2 auth can-i list wggatewayservers.networking.liqo.io --all-namespaces
```

---

## 5) Lettura telemetria reale (manuale)

Obiettivo: verificare che i provider espongano valori utili (status/latency).

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -A
kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -A
```

Dettaglio con latenza e stato:

```powershell
kubectl --kubeconfig $KC_PROVIDER1 get connections.networking.liqo.io -A -o jsonpath="{range .items[*]}{.metadata.namespace}{'/'}{.metadata.name}{' status='}{.status.value}{' latency='}{.status.latency.value}{' ts='}{.status.latency.timestamp}{'\n'}{end}"

kubectl --kubeconfig $KC_PROVIDER2 get connections.networking.liqo.io -A -o jsonpath="{range .items[*]}{.metadata.namespace}{'/'}{.metadata.name}{' status='}{.status.value}{' latency='}{.status.latency.value}{' ts='}{.status.latency.timestamp}{'\n'}{end}"
```

Atteso:
- `status` valorizzato (`Connected`/`Connecting`/`Error`)
- `latency` valorizzata quando la connessione e monitorata

---

## 6) Test rapido modalita stand-alone toy-labeler

Obiettivo: verificare che dal tuo host puoi leggere da tutti i cluster con gli stessi kubeconfig che usera la logica Auto.

```powershell
kubectl --kubeconfig $KC_CONSUMER get nodes
kubectl --kubeconfig $KC_PROVIDER1 get nodes
kubectl --kubeconfig $KC_PROVIDER2 get nodes
```

Se tutti passano, il processo stand-alone puo creare client multipli e interrogare consumer + provider.

---

## 7) Test se il controller gira nel cluster consumer

Obiettivo: verificare accesso remoto dal pod controller (non dal laptop).

Prerequisito: kubeconfig provider montati nel pod (es. Secret).

1. Entra nel pod del controller:

```powershell
kubectl --kubeconfig $KC_CONSUMER -n toy-labeler-system get pods
kubectl --kubeconfig $KC_CONSUMER -n toy-labeler-system exec -it <TOY_LABELER_POD> -- sh
```

2. Dal pod, prova i check verso provider (esempio path montato):

```sh
kubectl --kubeconfig /etc/toy-labeler/remote/provider1.kubeconfig get --raw=/healthz
kubectl --kubeconfig /etc/toy-labeler/remote/provider2.kubeconfig get --raw=/healthz
kubectl --kubeconfig /etc/toy-labeler/remote/provider1.kubeconfig get connections.networking.liqo.io -A
kubectl --kubeconfig /etc/toy-labeler/remote/provider2.kubeconfig get connections.networking.liqo.io -A
```

Se questo test passa, il controller in-cluster ha davvero accesso ai provider.

---

## 8) Diagnostica veloce errori

- `Unauthorized`
  - credenziali non valide (token/cert/scadute)

- `Forbidden`
  - autenticato ma RBAC insufficiente (`can-i` = no)

- `Unable to connect` / timeout / refused
  - endpoint API non raggiungibile o kubeconfig errato

- risorsa `connections.networking.liqo.io` non trovata
  - modulo networking non pronto o CRD assente

---

## 9) Criterio di OK finale

Puoi considerare "accesso telemetrico pronto" quando sono veri tutti:

1. healthz provider1/provider2 = ok
2. `auth can-i get/list/watch connections.networking.liqo.io` = yes su entrambi
3. `get connections -A` ritorna dati su entrambi
4. (se in-cluster) gli stessi test passano anche dall'interno del pod controller

Se uno di questi punti fallisce, Auto deve restare in fallback conservativo (non cambiare path).
