# Auto Telemetry Solution (Cluster-Wide)

## 1. Obiettivo
Questa soluzione descrive come far funzionare la modalita `Auto` in modo robusto, usando solo analisi telemetriche.

In `Auto`, il controller deve scegliere automaticamente tra:
- percorso `central` (hub-and-spoke)
- percorso `direct` (provider-to-provider)

senza usare policy manuali `force-*`.

## 2. Problema chiave
Il cluster centrale non vede direttamente tutto il traffico del percorso `direct`.

Quindi il centro **non puo misurare da solo** la qualita del direct path.
La misura deve essere:
- **distribuita** (ai bordi, cioe sui cluster che usano/vedono il percorso)
- **aggregata centralmente** (il controller `Auto` riceve report e decide)

## 3. Principio di design
Separare due ruoli:
- Misura: fatta localmente da agenti telemetrici sui cluster coinvolti
- Decisione: fatta centralmente dal controller `Auto`

In altre parole:
- il bordo produce la verita osservata
- il centro produce la decisione finale

## 4. Architettura logica
Componenti minimi:

1. `Telemetry Agent` (uno per cluster)
- raccoglie metriche per path `central` e `direct`
- usa finestre temporali corte
- pubblica report periodici

2. `Telemetry Aggregator` (lato controller Auto)
- riceve report da tutti i cluster
- valida freschezza e completezza dati
- calcola score per `central` e `direct`

3. `Auto Decision Engine`
- confronta score
- applica soglie e stabilizzazione temporale
- decide se impostare o togliere `use-direct-connections=true`

## 5. Metriche minime (semplici ma utili)
Per ogni path (`central`, `direct`) servono almeno:

- `rtt_ms_avg`: latency media nella finestra
- `loss_pct`: percentuale pacchetti persi
- `timeout_pct`: percentuale richieste in timeout
- `sample_count`: numero campioni usati
- `last_update_ts`: timestamp ultimo aggiornamento

Perche bastano queste 3 metriche:
- RTT rappresenta performance percepita
- Loss rappresenta affidabilita rete
- Timeout rappresenta impatto applicativo

## 6. Formato report consigliato
Esempio JSON di report inviato da un agente:

```json
{
  "srcCluster": "provider1",
  "dstCluster": "provider2",
  "pathType": "direct",
  "windowSec": 60,
  "samplePeriodSec": 10,
  "rtt_ms_avg": 18.4,
  "loss_pct": 0.2,
  "timeout_pct": 0.0,
  "sample_count": 6,
  "last_update_ts": "2026-04-22T10:12:30Z"
}
```

Note pratiche:
- stesso formato per `pathType=central`
- almeno un report per coppia cluster e per tipo path
- se un path non e misurabile, report con stato `unavailable`

## 7. Data quality gate (obbligatorio)
Prima del confronto, il controller deve verificare:

1. Freschezza
- `now - last_update_ts <= 30s`

2. Completezza
- sono presenti report validi sia per `central` che per `direct`

3. Campionamento minimo
- `sample_count >= 3` nella finestra corrente

Se uno dei gate fallisce:
- nessun cambio decisione
- fallback conservativo: mantenere stato corrente (o central se prima decisione)

## 8. Normalizzazione semplice
Per confrontare metriche diverse, normalizzare in [0,1].

Esempio pratico:

- `RTT_norm = min(rtt_ms_avg / RTT_ref, 1)`
- `LOSS_norm = min(loss_pct / LOSS_ref, 1)`
- `TO_norm = min(timeout_pct / TO_ref, 1)`

Valori iniziali ragionevoli:
- `RTT_ref = 100 ms`
- `LOSS_ref = 5%`
- `TO_ref = 2%`

## 9. Score telemetrico per path
Score piu basso = path migliore.

```text
Score(path) = 0.6 * RTT_norm + 0.3 * LOSS_norm + 0.1 * TO_norm
```

Motivazione pesi:
- RTT pesa di piu in molti workload interattivi
- Loss pesa quasi quanto RTT
- Timeout pesa meno ma protegge da stati patologici

## 10. Regola decisionale Auto
Calcolare:

```text
delta = Score(central) - Score(direct)
```

Interpretazione:
- `delta > 0`: direct migliore
- `delta < 0`: central migliore

Soglia minima per evitare micro-switch:
- `|delta| >= 0.10` (10%)

Decisione base:
- se `delta >= 0.10` => candidato `direct`
- se `delta <= -0.10` => candidato `central`
- altrimenti => nessun cambio

## 11. Stabilizzazione temporale (anti-flap)
Per cambiare davvero path, non basta una sola finestra.

Regola semplice:
- il candidato deve restare uguale per 3 finestre consecutive

Con finestra da 60s:
- tempo minimo prima dello switch = circa 3 minuti

Questo riduce cambiamenti dovuti a spike temporanei.

## 12. Logica operativa completa (pseudo-flow)

```text
Ogni 10s:
  1) Raccogli report telemetrici da agenti
  2) Applica data quality gate
  3) Se gate fallisce: mantieni decisione corrente
  4) Se gate passa:
       - calcola Score(central), Score(direct)
       - calcola delta
       - determina candidato (direct/central/none)
  5) Aggiorna contatore finestre consecutive del candidato
  6) Se candidato valido per 3 finestre:
       - applica decisione
       - direct  => set use-direct-connections=true
       - central => remove use-direct-connections
```

## 13. Cosa scrivere nei log (essenziale)
Per ogni decisione (o mancata decisione) loggare:

- path corrente
- score central e direct
- delta
- esito quality gate
- contatore finestre consecutive
- motivo finale (`switch`, `hold`, `insufficient-data`)

Questo rende il comportamento spiegabile in demo e in tesi.

## 14. Failure mode e fallback
Casi tipici:

1. Dati direct mancanti
- azione: non switchare a direct

2. Dati stantii
- azione: freeze decisione

3. Report contraddittori tra agenti
- azione: usare mediana o scartare outlier grossi

4. Timeout in aumento improvviso
- azione: preferire central se il fenomeno persiste 3 finestre

## 15. Parametri iniziali consigliati
Set iniziale semplice:

- `samplePeriodSec = 10`
- `windowSec = 60`
- `freshnessMaxAgeSec = 30`
- `minSampleCount = 3`
- `decisionMargin = 0.10`
- `confirmWindows = 3`
- pesi score: `0.6 / 0.3 / 0.1`

Questi valori sono facili da spiegare e abbastanza robusti per una prima demo.

## 16. Risultato atteso
Con questa soluzione, `Auto`:
- non dipende da assunzioni sul path visto dal centro
- usa telemetria reale osservata ai bordi
- decide centralmente in modo coerente e tracciabile
- evita flapping con regole semplici

In sintesi:
- misura distribuita
- decisione centralizzata
- fallback conservativo
- comportamento stabile
