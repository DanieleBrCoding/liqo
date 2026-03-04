package toylabelercontroller

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Struct del controller, contiene il client per parlare con Kubernetes
type ToyLabelerReconciler struct {
	client.Client
}

// Reconcile viene chiamato ogni volta che un Service cambia
func (r *ToyLabelerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//Ignora i namespace di sistema
	if req.Namespace == "kube-system" || req.Namespace == "liqo" {
		return ctrl.Result{}, nil
	}

	//Legge il Service dal cluster
	svc := &corev1.Service{}
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	//Se ha già l'annotazione, non fa nulla
	if svc.Annotations["use-direct-connections"] == "true" {
		return ctrl.Result{}, nil
	}

	//Inizializza la mappa annotations se è nil (evita crash)
	annotations := svc.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	//Salva il timestamp e torna tra 5s
	firstSeen, found := annotations["toy-labeler/firstSeen"]
	if !found {
		patch := client.MergeFrom(svc.DeepCopy())
		annotations["toy-labeler/firstSeen"] = time.Now().UTC().Format(time.RFC3339)
		svc.SetAnnotations(annotations)
		if err := r.Patch(ctx, svc, patch); err != nil {
			return ctrl.Result{}, err
		}
		klog.Infof("Service %q prima volta, annotazione tra 5s", req.NamespacedName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	//Controlla se sono passati 5 secondi
	ts, err := time.Parse(time.RFC3339, firstSeen)
	if err != nil {
		return ctrl.Result{}, err
	}
	if time.Since(ts) < 5*time.Second {
		return ctrl.Result{RequeueAfter: 5*time.Second - time.Since(ts)}, nil
	}

	//5s passati: applica l'annotazione e rimuove firstSeen
	patch := client.MergeFrom(svc.DeepCopy())
	annotations["use-direct-connections"] = "true"
	delete(annotations, "toy-labeler/firstSeen")
	svc.SetAnnotations(annotations)
	if err := r.Patch(ctx, svc, patch); err != nil {
		return ctrl.Result{}, err
	}
	klog.Infof("Service %q annotato con use-direct-connections=true", req.NamespacedName)

	return ctrl.Result{}, nil
}

func (r *ToyLabelerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Complete(r)
}
