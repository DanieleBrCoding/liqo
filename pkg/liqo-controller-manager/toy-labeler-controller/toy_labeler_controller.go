package toylabelercontroller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// Struct del controller, contiene il client per parlare con Kubernetes
type ToyLabelerReconciler struct {
	client.Client
}

const (
	directAnnotation       = "use-direct-connections"
	directPolicyAnnotation = "direct-connection-policy"
	policyAuto             = "auto"
	policyForceCentral     = "force-central"
	policyForceDirect      = "force-direct"
)

// Reconcile viene chiamato ogni volta che un Service cambia
func (r *ToyLabelerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	//Ignora i namespace di sistema
	if req.Namespace == "kube-system" || req.Namespace == "liqo" {
		return ctrl.Result{}, nil
	}

	// Ignora il service di default Kubernetes.
	if req.Namespace == "default" && req.Name == "kubernetes" {
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

	endpointCount, err := r.countEndpoints(ctx, svc)
	if err != nil {
		klog.Errorf("Errore nel conteggio degli endpoint per il Service %q: %v", req.NamespacedName, err)
		return ctrl.Result{}, err
	}

	policy := policyAuto
	if svc.Annotations != nil {
		if value, found := svc.Annotations[directPolicyAnnotation]; found && value != "" {
			policy = value
		}
	}

	switch policy {
	case policyForceCentral:
		if svc.Annotations != nil && svc.Annotations[directAnnotation] != "" {
			patch := client.MergeFrom(svc.DeepCopy())
			annotations := svc.Annotations
			delete(annotations, directAnnotation)
			svc.SetAnnotations(annotations)
			if err := r.Patch(ctx, svc, patch); err != nil {
				return ctrl.Result{}, err
			}
			klog.Infof("Service %q policy=force-central: rimossa l'annotazione use-direct-connections", req.NamespacedName)
		}
		return ctrl.Result{}, nil
	case policyForceDirect:
		if svc.Annotations != nil && svc.Annotations[directAnnotation] == "true" {
			return ctrl.Result{}, nil
		}
		annotations := svc.Annotations
		if annotations == nil {
			annotations = make(map[string]string)
		}
		patch := client.MergeFrom(svc.DeepCopy())
		annotations[directAnnotation] = "true"
		svc.SetAnnotations(annotations)
		if err := r.Patch(ctx, svc, patch); err != nil {
			return ctrl.Result{}, err
		}
		klog.Infof("Service %q policy=force-direct: aggiunta l'annotazione use-direct-connections=true", req.NamespacedName)
		return ctrl.Result{}, nil
	case policyAuto:
		// fallback alla logica automatica basata su endpointCount
	default:
		klog.Infof("Service %q policy sconosciuta %q: uso comportamento auto", req.NamespacedName, policy)
	}

	if endpointCount <= 1 {
		// Rimuovi l'annotazione se presente
		if svc.Annotations != nil && svc.Annotations[directAnnotation] != "" {
			patch := client.MergeFrom(svc.DeepCopy())
			annotations := svc.Annotations
			delete(annotations, directAnnotation)
			svc.SetAnnotations(annotations)
			if err := r.Patch(ctx, svc, patch); err != nil {
				return ctrl.Result{}, err
			}
			klog.Infof("Service %q ha solo %d endpoint, rimossa l'annotazione use-direct-connections", req.NamespacedName, endpointCount)
		} else {
			// Evita il doppio messaggio in fase iniziale (tipico passaggio 0 -> 1).
			if endpointCount == 1 {
				klog.Infof("Service %q ha %d endpoint, non aggiunta l'annotazione use-direct-connections=true", req.NamespacedName, endpointCount)
			}
		}
		return ctrl.Result{}, nil
	}

	// Evita patch inutili se l'annotazione e' gia' corretta.
	if svc.Annotations != nil && svc.Annotations[directAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	// Annota con use-direct-connections=true
	annotations := svc.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}
	patch := client.MergeFrom(svc.DeepCopy())
	annotations[directAnnotation] = "true"
	svc.SetAnnotations(annotations)
	if err := r.Patch(ctx, svc, patch); err != nil {
		return ctrl.Result{}, err
	}
	klog.Infof("Service %q ha %d endpoint, aggiunta l'annotazione use-direct-connections=true", req.NamespacedName, endpointCount)

	return ctrl.Result{}, nil
}

func (r *ToyLabelerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Service{}).
		Watches(
			&discoveryv1.EndpointSlice{},
			handler.EnqueueRequestsFromMapFunc(r.mapEndpointSliceToService),
		).
		Complete(r)
}

// mapEndpointSliceToService mappa ogni EndpointSlice al Service owner
func (r *ToyLabelerReconciler) mapEndpointSliceToService(ctx context.Context, obj client.Object) []ctrl.Request {
	eps, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		return nil
	}

	// L'EndpointSlice ha il label kubernetes.io/service-name che punta al Service
	serviceName, found := eps.Labels[discoveryv1.LabelServiceName]
	if !found {
		return nil
	}

	return []ctrl.Request{
		{
			NamespacedName: client.ObjectKey{
				Namespace: eps.Namespace,
				Name:      serviceName,
			},
		},
	}
}

// countEndpoints conta il numero totale di endpoint associati a un Service
// leggendo gli EndpointSlice con il label kubernetes.io/service-name.
func (r *ToyLabelerReconciler) countEndpoints(ctx context.Context, svc *corev1.Service) (int, error) {
	epsList := &discoveryv1.EndpointSliceList{}
	err := r.List(ctx, epsList,
		client.InNamespace(svc.Namespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: svc.Name},
	)
	if err != nil {
		return 0, err
	}

	count := 0
	for i := range epsList.Items {
		count += len(epsList.Items[i].Endpoints)
	}
	return count, nil
}
