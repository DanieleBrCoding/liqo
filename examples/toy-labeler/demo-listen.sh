#!/usr/bin/env bash
# Resta in ascolto dei ping (ICMP echo-request) che attraversano i gateway Liqo di un cluster
# e indica se il traffico passa dal consumer (hub, percorso indiretto) o dal tunnel diretto.
# Uso: ./demo-listen.sh <consumer|provider1|provider2>
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

CLUSTER="${1:-}"
case "$CLUSTER" in
  consumer|provider1|provider2) ;;
  *) echo "Uso: $0 <consumer|provider1|provider2>"; exit 1 ;;
esac

need_cluster "$CLUSTER"
trap cleanup_sniff INT TERM EXIT

echo "${C}=== $CLUSTER: ascolto ICMP sui gateway Liqo ===${N}"

found=0
has_direct=0
while read -r ns pod remote; do
  [ -z "${pod:-}" ] && continue
  found=1
  if [ "$CLUSTER" = "consumer" ]; then
    tag="HUB: il traffico passa dal CONSUMER (gw verso $remote)"; color="$Y"
  elif [ "$remote" = "consumer" ]; then
    tag="INDIRETTO via CONSUMER (gw verso consumer)"; color="$Y"
  else
    tag="DIRETTO (gw verso $remote)"; color="$G"; has_direct=1
  fi
  info "$pod ($ns) -> $tag"
  sniff_gateway "$CLUSTER" "$ns" "$pod" "$tag" "$color"
done < <(gateway_pods "$CLUSTER")

if [ "$found" = 0 ]; then
  err "Nessun gateway Liqo in esecuzione su $CLUSTER (peering non attivo?). Controlla: liqoctl info --kubeconfig $(kcfg "$CLUSTER")"
  exit 1
fi
if [ "$CLUSTER" != "consumer" ] && [ "$has_direct" = 0 ]; then
  warn "Nessun gateway verso l'altro provider: il tunnel diretto non c'e' (passo 4 di ./setup-linux.sh, --direct-tunnel)."
fi

echo "${C}In attesa di pacchetti... (Ctrl+C per uscire)${N}"
wait
