#!/usr/bin/env bash
# Osserva ICMP e TCP porta 80 sui gateway Liqo; correlare orari, IP e porte.
# Uso: ./demo-listen.sh <consumer|provider1|provider2> [--http-only]
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/demo-lib.sh"

CLUSTER="${1:-}"
case "$CLUSTER:${2:-}:$#" in
  consumer::1|provider1::1|provider2::1) filter='icmp or tcp port 80'; traffic='ICMP e TCP porta 80' ;;
  consumer:--http-only:2|provider1:--http-only:2|provider2:--http-only:2) filter='tcp port 80'; traffic='solo TCP porta 80' ;;
  *) echo "Uso: $0 <consumer|provider1|provider2> [--http-only]" >&2; exit 1 ;;
esac

need_cluster "$CLUSTER"
trap cleanup_sniff INT TERM EXIT

echo "${C}=== $CLUSTER: ascolto $traffic sui gateway Liqo ===${N}"

found=0
has_direct=0
while read -r ns pod remote; do
  [ -z "${pod:-}" ] && continue
  found=1
  if [ "$CLUSTER" = "consumer" ]; then
    tag="CONSUMER: gw verso $remote"; color="$Y"
  elif [ "$remote" = "consumer" ]; then
    tag="gateway verso consumer"; color="$Y"
  else
    tag="gateway verso $remote"; color="$G"; has_direct=1
  fi
  info "$pod ($ns) -> $tag"
  sniff_gateway "$CLUSTER" "$ns" "$pod" "$tag" "$color" "$filter"
done < <(gateway_pods "$CLUSTER")

if [ "$found" = 0 ]; then
  err "Nessun gateway Liqo in esecuzione su $CLUSTER (peering non attivo?). Controlla: liqoctl info --kubeconfig $(kcfg "$CLUSTER")"
  exit 1
fi
if [ "$CLUSTER" != "consumer" ] && [ "$has_direct" = 0 ]; then
  warn "Nessun gateway verso l'altro provider: il tunnel diretto non c'e' (passo 4 di ./setup-linux.sh, --direct-tunnel)."
fi

echo "${C}Correla i pacchetti alle richieste HTTP tramite orario, IP e porta (Ctrl+C per uscire).${N}"
wait
