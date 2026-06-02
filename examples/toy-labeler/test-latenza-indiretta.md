# Test di Latenza: Connessione Indiretta (Hub-and-Spoke)

Questa guida illustra i comandi per misurare la latenza della connessione indiretta tra `Provider 1` e `Provider 2` passando attraverso il `Consumer`, sfruttando l'architettura Liqo.

> **Nota:** Si dà per scontato che l'infrastruttura (cluster, peering, namespace offload e pod client/server) sia già completamente configurata e funzionante.

## 1. Deploy del Service

Assicurati che il servizio sia stato deployato nel cluster Consumer. Questo esporrà i pod server del `Provider 2` ai pod client del `Provider 1`.

```yaml
# demo-server-svc.yaml
apiVersion: v1
kind: Service
metadata:
  name: demo-server
  namespace: hub-spoke-demo
  # L'assenza dell'annotazione use-direct-connections=true forza il routing indiretto
spec:
  selector:
    app: demo-server
  ports:
    - protocol: TCP
      port: 80
      targetPort: 80
  type: ClusterIP
```

Applica il manifest nel cluster Consumer:

```powershell
kubectl apply -f demo-server-svc.yaml --kubeconfig liqo_kubeconf_consumer
```

## 2. Misurazione della Latenza (Ping dal Provider 1)

Per misurare la latenza, devi eseguire un `ping` dal pod client (posto sul `Provider 1`) verso l'indirizzo IP del Server (che risiede sul `Provider 2`).

Poiché stiamo operando *esclusivamente* dal contesto del `Provider 1`, andremo a prelevare l'IP target (l'IP NATtato che instraderà il traffico nel tunnel di Liqo) direttamente dalle `EndpointSlice` riflesse sul `Provider 1`.

Esegui il seguente comando in un unico blocco:

```powershell
kubectl --kubeconfig liqo_kubeconf_provider1 -n hub-spoke-demo exec client-on-provider1 -c netshoot -- ping -c 5 $(kubectl --kubeconfig liqo_kubeconf_provider1 -n hub-spoke-demo get endpointslice -l kubernetes.io/service-name=demo-server -o jsonpath="{.items[0].endpoints[0].addresses[0]}")
```

### Cosa Aspettarsi

Poiché sul Service appena deployato non è presente l'annotazione `use-direct-connections=true`, il traffico seguirà il percorso **Provider 1 -> Consumer -> Provider 2**. 

L'output del comando ti mostrerà l'RTT (Round-Trip Time) tipico di questa connessione indiretta (hub-and-spoke).
