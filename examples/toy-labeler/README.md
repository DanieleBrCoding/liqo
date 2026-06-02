$KC_PROVIDER1 = "examples/toy-labeler/liqo_kubeconf_provider1"
$KC_CONSUMER  = "examples/toy-labeler/liqo_kubeconf_consumer"
$NS = "hub-spoke-demo"

# tieni il contesto su provider1
$env:KUBECONFIG = $KC_PROVIDER1
kubectl config current-context

# ping e2e via consumer (indiretto)
$serverIP = kubectl --kubeconfig $KC_CONSUMER -n $NS get pod -l app=demo-server -o jsonpath="{.items[0].status.podIP}"
kubectl --kubeconfig $KC_CONSUMER -n $NS exec client-on-provider2 -c netshoot -- ping -c 20 $serverIP# Toy-Labeler Demo — Guida completa

Questa guida spiega come configurare un ambiente multi-cluster con Liqo (3 cluster kind), dimostrare il routing hub-and-spoke, e testare il passaggio al tunnel diretto tramite il controller **toy-labeler**.

---

## Indice

1. [Prerequisiti](#prerequisiti)
2. [Architettura](#architettura)
3. [Setup dei 3 cluster con Liqo](#setup-dei-3-cluster-con-liqo)
4. [Demo Hub-and-Spoke](#demo-hub-and-spoke)
5. [Connessione diretta tra i provider](#connessione-diretta-tra-i-provider)
6. [Demo con il toy-labeler (Direct Path)](#demo-con-il-toy-labeler-direct-path)
7. [Comandi utili](#comandi-utili)
8. [Cleanup](#cleanup)
9. [Troubleshooting](#troubleshooting)

---

## Prerequisiti

| Tool | Versione testata | Installazione |
|------|-----------------|---------------|
| Docker Desktop | - | [docker.com](https://www.docker.com/products/docker-desktop/) |
| kind | v0.31.0 | `go install sigs.k8s.io/kind@v0.31.0` |
| kubectl | v1.30+ | [kubernetes.io](https://kubernetes.io/docs/tasks/tools/) |
| liqoctl | dal branch | `go install .\cmd\liqoctl\` |
| Go | 1.22+ | [go.dev](https://go.dev/dl/) |

> **Importante:** `liqoctl` va compilato dal branch `no-fcc-implementation` del fork, non dalla release ufficiale.

```powershell
# Compila liqoctl dal branch corrente
go install .\cmd\liqoctl\
```

---

## Architettura

```
        ┌──────────┐
        │ CONSUMER │  (cluster centrale / hub)
        └────┬─┬───┘
   peering   │ │   peering
     ┌───────┘ └────────┐
     ▼                  ▼
┌──────────┐      ┌──────────┐
│PROVIDER1 │      │PROVIDER2 │
│(server)  │      │(client)  │
└──────────┘      └──────────┘
```

- **Consumer**: cluster centrale, fa peering con entrambi i provider
- **Provider1**: ospita il server (NGINX)
- **Provider2**: ospita il client (netshoot)
- I provider **non** hanno peering diretto (inizialmente)

### Subnet

| Cluster | Pod CIDR | Service CIDR |
|---------|----------|-------------|
| consumer | 10.200.0.0/16 | 10.60.0.0/16 |
| provider1 | 10.201.0.0/16 | 10.61.0.0/16 |
| provider2 | 10.202.0.0/16 | 10.62.0.0/16 |

---

## Setup dei 3 cluster con Liqo

Lo script crea i 3 cluster, installa Liqo, fa il peering, **builda le immagini locali dal branch** (virtual-kubelet e liqo-controller-manager) e le carica nei cluster.

```powershell
.\examples\toy-labeler\setup-liqo-minimal.ps1
```

Lo script esegue 5 step:
1. Creazione dei 3 cluster kind
2. Installazione Liqo v1.0.1 su tutti
3. Peering consumer↔provider1 e consumer↔provider2
4. Build immagini locali dal branch e caricamento in kind
5. Restart dei pod per usare le immagini locali

> **Perché le immagini locali?** Il branch `no-fcc-implementation` contiene codice custom nel `virtual-kubelet` (reflection EndpointSlice con supporto connessioni dirette) e nel `liqo-controller-manager` (mapping EndpointSlice). Le immagini ufficiali v1.0.1 non contengono questo codice.

### File di configurazione

- `manifests/cluster-consumer.yaml` — config kind per il consumer
- `manifests/cluster-provider1.yaml` — config kind per provider1
- `manifests/cluster-provider2.yaml` — config kind per provider2

### Kubeconfig generati

Dopo il setup, i kubeconfig sono in `examples/toy-labeler/`:
- `liqo_kubeconf_consumer`
- `liqo_kubeconf_provider1`
- `liqo_kubeconf_provider2`

---

## Demo Hub-and-Spoke

Dimostra che il traffico provider2→provider1 **passa obbligatoriamente dal consumer** (non c'è peering diretto tra provider).

```powershell
.\examples\toy-labeler\demo-hub-spoke.ps1
```

Lo script:
1. Verifica il peering (consumer vede virtual-node provider1/provider2, i provider non si vedono tra loro)
2. Crea e offloada il namespace `hub-spoke-demo`
3. Deploya NGINX su provider1 (tramite node affinity su `liqo.io/remote-cluster-id=provider1`)
4. Deploya netshoot su provider2 (tramite node affinity su `liqo.io/remote-cluster-id=provider2`)
5. Curl dal client al server → HTTP 200
6. Controprova: i provider non hanno virtual-node dell'altro provider

### Verifica con tcpdump

Per provare che il traffico transita dal consumer, servono **2 terminali**:

**Terminale 1 — tcpdump sul gateway del consumer:**
```powershell
# Prima trova il nome del pod gateway
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer | Select-String "gw"

# Poi lancia tcpdump (sostituisci il nome del pod)
kubectl exec -n liqo-tenant-provider1 <GW-POD-NAME> --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -- tcpdump -i any -n port 80
```

**Terminale 2 — curl dal client:**
```powershell
kubectl exec -n hub-spoke-demo client-on-provider2 -c netshoot --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
```

Se il tcpdump mostra pacchetti, il traffico passa dal consumer. Le interfacce nel tcpdump:
- `liqo.xxxxx In` = pacchetto in arrivo dal tunnel di provider2
- `liqo-tunnel Out` = pacchetto in uscita verso provider1
- E viceversa per le risposte

---

## Connessione diretta tra i provider

Crea un tunnel di rete diretto tra provider1 e provider2 (senza peering completo, senza virtual-node):

```powershell
liqoctl network connect --kubeconfig examples\toy-labeler\liqo_kubeconf_provider1 --remote-kubeconfig examples\toy-labeler\liqo_kubeconf_provider2 --gw-server-service-type NodePort
```

Verifica che i gateway tra provider esistano:
```powershell
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_provider1 | Select-String "gw"
```

> **Nota:** Anche con il tunnel diretto presente, il traffico continua a passare dal consumer finché il service non ha l'annotazione `use-direct-connections=true`.

---

## Demo con il toy-labeler (Direct Path)

### Come funziona il toy-labeler

Il controller (`pkg/liqo-controller-manager/toy-labeler-controller/toy_labeler_controller.go`) osserva tutti i Service nel cluster. Quando ne vede uno nuovo:

1. Annota il service con `toy-labeler/firstSeen=<timestamp>`
2. Dopo **5 secondi**, aggiunge l'annotazione `use-direct-connections=true` e rimuove `firstSeen`
3. I componenti Liqo custom (virtual-kubelet, controller-manager) reagiscono all'annotazione e riconfigurano il routing per usare il tunnel diretto (se disponibile)

### Demo passo-passo

**Prerequisiti:** aver eseguito `setup-liqo-minimal.ps1`, `demo-hub-spoke.ps1`, e `liqoctl network connect` (vedi sopra).

#### 1. Cancella il service esistente

```powershell
kubectl delete svc demo-server -n hub-spoke-demo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
```

#### 2. Avvia il toy-labeler in foreground (Terminale 1)

```powershell
$env:KUBECONFIG = "examples\toy-labeler\liqo_kubeconf_consumer"
.\toy-labeler.exe
```

Vedrai i log in tempo reale.

#### 3. Avvia tcpdump sul gateway del consumer (Terminale 2)

```powershell
# Trova il pod gateway
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer | Select-String "gw"

# Lancia tcpdump
kubectl exec -n liqo-tenant-provider1 <GW-POD-NAME> --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -- tcpdump -i any -n port 80
```

#### 4. Crea il service fresco (Terminale 3)

```powershell
@"
apiVersion: v1
kind: Service
metadata:
  name: demo-server
  namespace: hub-spoke-demo
spec:
  selector:
    app: demo-server
  ports:
    - port: 80
      targetPort: 80
  type: ClusterIP
"@ | kubectl apply --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -f -
```

#### 5. Subito (entro 5s): curl — traffico passa dal consumer

```powershell
kubectl exec -n hub-spoke-demo client-on-provider2 -c netshoot --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
```

→ Dovresti vedere pacchetti nel tcpdump (Terminale 2)

#### 6. Aspetta ~5 secondi e verifica l'annotazione

Il toy-labeler (Terminale 1) mostrerà:
```
Service "hub-spoke-demo/demo-server" annotato con use-direct-connections=true
```

Verifica:
```powershell
kubectl get svc demo-server -n hub-spoke-demo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -o jsonpath='{.metadata.annotations}'
```

#### 7. Rifai il curl — traffico NON passa più dal consumer

```powershell
kubectl exec -n hub-spoke-demo client-on-provider2 -c netshoot --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
```

→ **Nessun pacchetto** nel tcpdump = il traffico usa il tunnel diretto provider1↔provider2

### Alternativa: deploy come pod nel cluster

Invece di eseguirlo in foreground, puoi deployarlo come pod:

```powershell
# Build immagine (una tantum)
docker build -t toy-labeler:latest -f build/liqo/Dockerfile .
# NOTA: serve prima compilare il binario:
# $env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
# go build -ldflags="-s -w" -o bin/amd64/toy-labeler_linux_amd64 ./cmd/toy-labeler

# Carica in kind
kind load docker-image toy-labeler:latest --name consumer

# Deploy
kubectl apply -f examples\toy-labeler\manifests\toy-labeler-deploy.yaml --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer

# Controlla i log
kubectl logs -f -n toy-labeler-system -l app=toy-labeler --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
```

---

## Comandi utili

### Gestione cluster

```powershell
# Vedere cluster attivi
kind get clusters

# Switchare contesto
kind export kubeconfig --name consumer
kind export kubeconfig --name provider1
kind export kubeconfig --name provider2
```

### Verifica stato Liqo

```powershell
# Pod Liqo sul consumer
kubectl get pods -n liqo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer

# Virtual nodes sul consumer
kubectl get nodes --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer

# Gateway pods (su consumer)
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer | Select-String "gw"

# Virtual-kubelet pods
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer | Select-String "vk-"
```

### Debug

```powershell
# Log virtual-kubelet per provider1
kubectl logs -n liqo-tenant-provider1 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer --tail=50

# Cercare log relativi a connessioni dirette
kubectl logs -n liqo-tenant-provider1 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer --tail=50 | Select-String "direct"

# Annotazioni di un service
kubectl get svc demo-server -n hub-spoke-demo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer -o jsonpath='{.metadata.annotations}'
```

### Connessione diretta

```powershell
# Crea tunnel diretto tra provider
liqoctl network connect --kubeconfig examples\toy-labeler\liqo_kubeconf_provider1 --remote-kubeconfig examples\toy-labeler\liqo_kubeconf_provider2 --gw-server-service-type NodePort

# Rimuovi tunnel diretto
liqoctl network disconnect --kubeconfig examples\toy-labeler\liqo_kubeconf_provider1 --remote-kubeconfig examples\toy-labeler\liqo_kubeconf_provider2
```

---

## Cleanup

### Solo il namespace demo

```powershell
kubectl delete namespace hub-spoke-demo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
```

### Tutti e 3 i cluster

```powershell
.\examples\toy-labeler\cleanup-liqo-minimal.ps1
```

> Lo script cancella **solo** i cluster consumer, provider1, provider2 (non tocca altri cluster kind eventualmente presenti).

---

## Troubleshooting

### Docker Desktop chiuso e cluster "rotti"

Se chiudi Docker Desktop e poi lo riavvii, i cluster kind riprendono ma Liqo potrebbe avere errori TLS (`x509: certificate signed by unknown authority`). Soluzione: ricreare i cluster.

```powershell
.\examples\toy-labeler\cleanup-liqo-minimal.ps1
.\examples\toy-labeler\setup-liqo-minimal.ps1
```

### Virtual nodes NotReady

Controlla i log del virtual-kubelet:
```powershell
kubectl logs -n liqo-tenant-provider1 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer --tail=20
```

Se ci sono errori TLS, ricrea i cluster (vedi sopra).

### curl dice "Couldn't resolve host" (exit code 6)

Il service non esiste ancora nel namespace, oppure il DNS non ha propagato. Verifica:
```powershell
kubectl get svc -n hub-spoke-demo --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
```

### kubectl exec fallisce con "container not found"

Aggiungi `-c netshoot` per specificare il container (necessario con il virtual kubelet):
```powershell
kubectl exec ... -c netshoot -- curl ...
```

### Immagini Liqo senza codice custom

Se il virtual-kubelet non reagisce a `use-direct-connections=true`, probabilmente sta usando l'immagine ufficiale. Verifica:
```powershell
kubectl logs -n liqo-tenant-provider1 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer --tail=50 | Select-String "direct"
```

Se non trovi log con "direct", ricarica le immagini locali:
```powershell
# Dal root del progetto
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags="-s -w" -o bin/amd64/virtual-kubelet_linux_amd64 ./cmd/virtual-kubelet
Remove-Item Env:\GOOS; Remove-Item Env:\GOARCH; Remove-Item Env:\CGO_ENABLED

docker build --build-arg COMPONENT=virtual-kubelet -t ghcr.io/liqotech/virtual-kubelet:v1.0.1 -f build/liqo/Dockerfile .
kind load docker-image ghcr.io/liqotech/virtual-kubelet:v1.0.1 --name consumer

# Cancella i pod per forzare il restart con l'immagine nuova
kubectl delete pod -n liqo-tenant-provider1 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
kubectl delete pod -n liqo-tenant-provider2 -l app.kubernetes.io/name=virtual-kubelet --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer
```

---

## Struttura file

```
examples/toy-labeler/
├── README.md                      ← questa guida
├── setup-liqo-minimal.ps1         ← setup 3 cluster + Liqo + immagini locali
├── cleanup-liqo-minimal.ps1       ← cancella i 3 cluster
├── demo-hub-spoke.ps1             ← demo traffico hub-and-spoke
├── manifests/
│   ├── cluster-consumer.yaml      ← config kind consumer
│   ├── cluster-provider1.yaml     ← config kind provider1
│   ├── cluster-provider2.yaml     ← config kind provider2
│   └── toy-labeler-deploy.yaml    ← manifest per deploy controller come pod
└── liqo_kubeconf_*                ← kubeconfig generati (gitignore)
```
