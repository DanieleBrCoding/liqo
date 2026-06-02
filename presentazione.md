# Demo CLI - Direct Connection Policy

Guida step by step per la dimostrazione di domani.

## Descrizione del sistema

Nel mio scenario il workload server gira sul cluster provider1 ed espone il servizio HTTP `demo-server`.
Il workload client gira sul cluster provider2 e usa `curl` per generare traffico verso quel servizio.
I gateway di Liqo sono i componenti che instradano il traffico tra consumer e provider; servono per osservare se il percorso passa dal cluster centrale oppure viene ottimizzato tramite direct connection.

Nota su IPAM e instradamento: IPAM fornisce la traduzione tra IP originali e IP remappati che i gateway usano per instradare correttamente il traffico tra cluster.
Quindi IPAM non serve solo ad assegnare indirizzi, ma anche a rendere il path realmente raggiungibile nel dominio remoto.
Il problema che osservavo era temporale: Service/EndpointSlice erano gia' aggiornati, ma la mappatura IPAM non era ancora completa.
Risultato: nei primi secondi il controller poteva scegliere il direct path con informazioni parziali, e il comportamento risultava non stabile fino al completamento della mappa.

## 1. Avvia il controller standalone

In un terminale dedicato:

```powershell
$env:KUBECONFIG="examples/toy-labeler/liqo_kubeconf_consumer"
.\toy-labeler.exe
```

Lascia questo terminale aperto: qui vedrai i log del controller.

Se devi creare il Service della demo:

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
"@ | kubectl apply --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -f -
```

Se devi rimuoverlo e ricrearlo durante la dimostrazione:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo delete svc demo-server
```

## 2. Identifica i gateway

Serve prima di fare tcpdump o verifiche sul path.

Trova i pod gateway nel consumer:

```powershell
kubectl get pods --all-namespaces --kubeconfig examples\toy-labeler\liqo_kubeconf_consumer | Select-String "gw"
```

Annotati i nomi dei pod gateway, in particolare quello del namespace `liqo-tenant-consumer`.

Comando rapido per il listening su porta 80 (tcpdump) sul gateway consumer:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer exec -n liqo-tenant-consumer <NOME_POD_GATEWAY_CONSUMER> -- tcpdump -i any -n port 80
```

Comando rapido curl dal client verso il Service:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer exec -n hub-spoke-demo client-on-provider2 -c netshoot -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
```

## 3. Verifica la policy e lo stato iniziale del Service

Imposta la policy `auto` e parti da 1 replica:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo annotate svc demo-server direct-connection-policy=auto --overwrite
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo scale deployment server-on-provider1 --replicas=1
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo rollout status deployment/server-on-provider1
```

Controlla annotazione tecnica e policy:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
```

Atteso: `policy=auto direct=` oppure `direct` assente.

## 4. Caso auto con 3 repliche

Scali a 3 repliche e osservi il log del controller:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo scale deployment server-on-provider1 --replicas=3
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo rollout status deployment/server-on-provider1
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
```

Atteso: `use-direct-connections=true`.

## 5. Analizza il traffico sui gateway

Prima identifica il pod gateway consumer trovato al punto 2, poi esegui tcpdump:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer exec -n liqo-tenant-consumer <NOME_POD_GATEWAY_CONSUMER> -- tcpdump -i any -n port 80
```

Se vuoi osservare anche il gateway lato provider, usa il kubeconfig del provider corrispondente:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_provider1 exec -n liqo-tenant-provider1 <NOME_POD_GATEWAY_PROVIDER1> -- tcpdump -i any -n port 80
```

Se il traffico è davvero diretto, sul gateway consumer dovresti vedere molto meno traffico quando `use-direct-connections=true`.

## 6. Verifica applicativa

Dal client nel consumer esegui una chiamata HTTP:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer exec -n hub-spoke-demo client-on-provider2 -c netshoot -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
```

Ripeti la chiamata dopo aver cambiato policy o replica per mostrare l'effetto sulla rete.

## 7. Caso force-central con 3 repliche

Forza il traffico via cluster centrale:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo annotate svc demo-server direct-connection-policy=force-central --overwrite
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo scale deployment server-on-provider1 --replicas=3
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo rollout status deployment/server-on-provider1
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
```

Atteso: `use-direct-connections` assente.

## 8. Caso force-direct con 1 replica

Forza la direct connection anche se c'è un solo endpoint:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo annotate svc demo-server direct-connection-policy=force-direct --overwrite
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo scale deployment server-on-provider1 --replicas=1
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo rollout status deployment/server-on-provider1
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
```

Atteso: `use-direct-connections=true`.

## 9. Comandi rapidi di controllo

Per vedere gli EndpointSlice del Service:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get endpointslice -l kubernetes.io/service-name=demo-server -o wide
```

Per vedere solo quante endpoint ci sono nei slice:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get endpointslice -l kubernetes.io/service-name=demo-server -o jsonpath="{range .items[*]}{.metadata.name}{' endpoints='}{range .endpoints[*]}x{end}{'\n'}{end}"
```

Per vedere il numero totale di endpoint reali associati al Service:

```powershell
((kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get endpointslice -l kubernetes.io/service-name=demo-server -o jsonpath="{range .items[*].endpoints[*]}x{end}").Length)
```

Per vedere le annotazioni correnti del Service:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="{.metadata.annotations}{'\n'}"
```

## 10. Sequenza minima da mostrare in diretta

Se hai poco tempo, fai solo questa sequenza:

```powershell
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo annotate svc demo-server direct-connection-policy=auto --overwrite
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo scale deployment server-on-provider1 --replicas=3
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer exec -n hub-spoke-demo client-on-provider2 -c netshoot -- curl -s http://demo-server.hub-spoke-demo.svc.cluster.local
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo annotate svc demo-server direct-connection-policy=force-central --overwrite
kubectl --kubeconfig examples/toy-labeler/liqo_kubeconf_consumer -n hub-spoke-demo get svc demo-server -o jsonpath="policy={.metadata.annotations.direct-connection-policy} direct={.metadata.annotations.use-direct-connections}{'\n'}"
```