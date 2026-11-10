#!/usr/bin/env bash
# Imposta il contesto kubectl corrente sul cluster kind "provider1".
set -euo pipefail

CLUSTER="provider1"

command -v kind >/dev/null || { echo "[ERROR] kind non trovato." >&2; exit 1; }
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" \
  || { echo "[ERROR] Cluster '$CLUSTER' non trovato (container fermi? prova: docker start ${CLUSTER}-control-plane ${CLUSTER}-worker)." >&2; exit 1; }

kind export kubeconfig --name "$CLUSTER" >/dev/null
kubectl config use-context "kind-$CLUSTER"
kubectl get nodes
