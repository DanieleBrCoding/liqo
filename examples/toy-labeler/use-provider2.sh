#!/usr/bin/env bash
# Imposta il contesto kubectl corrente sul cluster kind "provider2".
set -euo pipefail

CLUSTER="provider2"

command -v kind >/dev/null || { echo "[ERROR] kind non trovato." >&2; exit 1; }
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" \
  || { echo "[ERROR] Cluster '$CLUSTER' non trovato (container fermi? prova: docker start ${CLUSTER}-control-plane ${CLUSTER}-worker)." >&2; exit 1; }

# Rigenera il contesto (utile dopo una ricreazione del cluster) e lo rinomina in "$CLUSTER"
kind export kubeconfig --name "$CLUSTER" >/dev/null 2>&1
kubectl config delete-context "$CLUSTER" >/dev/null 2>&1 || true
kubectl config rename-context "kind-$CLUSTER" "$CLUSTER" >/dev/null
kubectl config use-context "$CLUSTER"
kubectl get nodes
