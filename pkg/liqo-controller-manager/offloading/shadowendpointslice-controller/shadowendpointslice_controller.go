// Copyright 2019-2026 The Liqo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package shadowendpointslicectrl

import (
	"context"
	"fmt"
	"strings"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	liqov1beta1 "github.com/liqotech/liqo/apis/core/v1beta1"
	offloadingv1beta1 "github.com/liqotech/liqo/apis/offloading/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
	"github.com/liqotech/liqo/pkg/utils"
	clientutils "github.com/liqotech/liqo/pkg/utils/clients"
	directconnectioninfo "github.com/liqotech/liqo/pkg/utils/directconnection"
	foreigncluster "github.com/liqotech/liqo/pkg/utils/foreigncluster"
	"github.com/liqotech/liqo/pkg/utils/resource"
	"github.com/liqotech/liqo/pkg/virtualKubelet/forge"
)

const (
	ctrlFieldManager = "shadow-endpointslice-controller"
)

// Reconciler reconciles a ShadowEndpointSlice object.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=offloading.liqo.io,resources=shadowendpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core.liqo.io,resources=foreignclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups=core.liqo.io,resources=foreignclusters/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.liqo.io,resources=configurations,verbs=get;list;watch

// Reconcile ShadowEndpointSlices objects.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	nsName := req.NamespacedName
	klog.V(4).Infof("reconcile shadowendpointslice %q", nsName)

	var shadowEps offloadingv1beta1.ShadowEndpointSlice
	if err := r.Get(ctx, nsName, &shadowEps); err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).Infof("shadowendpointslice %q not found", nsName)
			return ctrl.Result{}, nil
		}
		klog.Errorf("an error occurred while getting shadowendpointslice %q: %v", nsName, err)
		return ctrl.Result{}, err
	}

	// Get ForeignCluster associated with the shadowendpointslice
	clusterID, ok := utils.GetClusterIDFromLabelsWithKey(shadowEps.Labels, forge.LiqoOriginClusterIDKey)
	if !ok {
		klog.Errorf("shadowendpointslice %q has no label %q", nsName, forge.LiqoOriginClusterIDKey)
		return ctrl.Result{}, fmt.Errorf("shadowendpointslice %q has no label %q", nsName, forge.LiqoOriginClusterIDKey)
	}
	fc, err := foreigncluster.GetForeignClusterByID(ctx, r.Client, clusterID)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Errorf("foreigncluster (id: %q) associated with shadowendpointslice %q not found", clusterID, nsName)
			return ctrl.Result{}, err
		}
		klog.Errorf("an error occurred while getting foreigncluster %q: %v", clusterID, err)
		return ctrl.Result{}, err
	}

	// Check network status of the foreigncluster
	networkReady := foreigncluster.IsNetworkingEstablishedOrDisabled(fc)

	// Check foreign API server status
	apiServerReady := foreigncluster.IsAPIServerReadyOrDisabled(fc)

	// Check if direct connections data is provided
	var remoteConnectionsData directconnectioninfo.DirectConnectionData
	if val, ok := shadowEps.Annotations[consts.DirectConnectionDataAnnotationKey]; ok {
		err := remoteConnectionsData.FromJSON([]byte(val))

		if err != nil {
			klog.Errorf("failed to unmarshal direct connection data for shadowendpointslice %q: %v", nsName, err)
			return ctrl.Result{}, err
		}
	}
	// Get the endpoints from the shadowendpointslice and remap them if necessary.
	// If the networking module is disabled, we do not need to remap the endpoints.
	remappedEndpoints := shadowEps.Spec.Template.Endpoints
	if foreigncluster.IsNetworkingModuleEnabled(fc) {

		rcindex := remoteConnectionsData.BuildIndex()

		// remap the endpoints if the network configuration of the remote cluster overlaps with the local one
		if err := MapEndpointsWithConfiguration(ctx, r.Client, clusterID, remappedEndpoints, rcindex); err != nil {
			klog.Errorf("an error occurred while remapping endpoints for shadowendpointslice %q: %v", nsName, err)
			return ctrl.Result{}, err
		}
	}

	// Direct connections data annotation is not propagated to the EndpointSlice
	annotations := forge.ForgeEndpointSliceAnnotations(&shadowEps, []string{consts.DirectConnectionDataAnnotationKey})

	// Forge the endpointslice given the shadowendpointslice
	newEps := discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      shadowEps.Name,
			Namespace: shadowEps.Namespace,
			Labels: labels.Merge(shadowEps.Labels, labels.Set{
				consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue}),
			Annotations: annotations,
		},
		AddressType: shadowEps.Spec.Template.AddressType,
		Endpoints:   remappedEndpoints,
		Ports:       shadowEps.Spec.Template.Ports,
	}

	// Depending on the current status of the foreign cluster, we update all endpoints' "Ready" conditions.
	// Endpoints are ready only if both the tunnel endpoint and the API server of the foreign cluster are ready.
	// Note: An endpoint is updated only if the shadowendpointslice endpoint has the condition "Ready" set
	// to True or nil. i.e: if the foreign cluster sets the endpoint condition "Ready" to False, also the local
	// endpoint condition is set to False regardless of the current status of the foreign cluster.
	endpointsReady := networkReady && apiServerReady
	for i := range newEps.Endpoints {
		endpoint := &newEps.Endpoints[i]
		if endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready {
			endpoint.Conditions.Ready = &endpointsReady
		}
	}

	// Determine ShadowEPS type for failover label management.
	// isIndirect: carries IPAM-remapped hub-and-spoke addresses; kube-proxy normally ignores it.
	// isDirect:   carries direct peer IPs and normally holds kubernetes.io/service-name.
	isIndirect := shadowEps.Labels[forge.IndirectEndpointSliceLabelKey] == "true"
	isDirect := !isIndirect && len(remoteConnectionsData.ByCluster) > 0

	// Get existing endpointslice if it is already been created from the shadowendpointslice
	var existingEps discoveryv1.EndpointSlice
	err = r.Get(ctx, nsName, &existingEps)

	switch {
	case errors.IsNotFound(err):
		// Create the endpointslice
		utilruntime.Must(ctrl.SetControllerReference(&shadowEps, &newEps, r.Scheme))

		resource.AddGlobalLabels(&newEps)
		resource.AddGlobalAnnotations(&newEps)

		// If this indirect EPS is being created while the direct connection is already in
		// failover mode, immediately inject kubernetes.io/service-name so kube-proxy uses
		// it from the very first moment without a flip-flop reconcile cycle.
		if isIndirect {
			if svcName, svcErr := r.getDirectServiceNameForFailover(ctx, shadowEps.Name, nsName.Namespace); svcErr == nil && svcName != "" {
				newEps.Labels[discoveryv1.LabelServiceName] = svcName
			}
		}

		if err := r.Create(ctx, &newEps, client.FieldOwner(ctrlFieldManager)); err != nil {
			klog.Errorf("unable to create endpointslice for shadowendpointslice %q: %v", klog.KObj(&shadowEps), err)
			return ctrl.Result{}, err
		}

		klog.Infof("created endpointslice %q for shadowendpointslice %q", klog.KObj(&newEps), klog.KObj(&shadowEps))

		return ctrl.Result{}, nil

	case err != nil:
		klog.Errorf("unable to get endpointslice %q: %v", nsName, err)
		return ctrl.Result{}, err

	default:
		klog.V(4).Infof("endpointslice %q found running, will update it", klog.KObj(&existingEps))

		// Failover label management: when the direct tunnel is down, the failover controller
		// sets DirectConnectionFailoverAnnotation on the direct EPS. The ShadowEPS controller
		// reacts by swapping which EPS holds kubernetes.io/service-name, transparently
		// redirecting kube-proxy traffic to the hub-and-spoke (indirect) path.
		//
		//   Direct ShadowEPS + failover active  → suppress service-name (kube-proxy ignores EPS)
		//   Indirect ShadowEPS + failover active → inject service-name  (kube-proxy uses EPS)
		//
		// Endpoint addresses and readiness conditions are still updated normally in both cases.
		if isDirect {
			if existingEps.Annotations[consts.DirectConnectionFailoverAnnotation] == "true" {
				klog.V(4).Infof("endpointslice %q is in failover mode, suppressing %q label",
					klog.KObj(&existingEps), discoveryv1.LabelServiceName)
				delete(newEps.Labels, discoveryv1.LabelServiceName)
			}
		} else if isIndirect {
			if svcName, svcErr := r.getDirectServiceNameForFailover(ctx, shadowEps.Name, nsName.Namespace); svcErr == nil && svcName != "" {
				klog.V(4).Infof("endpointslice %q: companion direct EPS is in failover mode, injecting %q label",
					klog.KObj(&existingEps), discoveryv1.LabelServiceName)
				newEps.Labels[discoveryv1.LabelServiceName] = svcName
			}
		}

		// Create Apply object for existing endpointslice
		epsApply := EndpointSliceApply(&newEps)

		if err := r.Patch(ctx, &existingEps, clientutils.Patch(epsApply),
			client.ForceOwnership, client.FieldOwner(ctrlFieldManager)); err != nil {
			klog.Errorf("unable to update endpointslice %q: %v", klog.KObj(&existingEps), err)
			return ctrl.Result{}, err
		}

		klog.Infof("updated endpointslice %q with success", klog.KObj(&existingEps))

		return ctrl.Result{}, nil
	}
}

// getForeignClusterEventHandler returns an event handler that reacts on ForeignClusters updates.
// In particular, it reacts on changes on the NetworkStatus condition.
func (r *Reconciler) getForeignClusterEventHandler(ctx context.Context) handler.EventHandler {
	return &handler.Funcs{
		UpdateFunc: func(_ context.Context, ue event.TypedUpdateEvent[client.Object], trli workqueue.TypedRateLimitingInterface[reconcile.Request]) {
			newForeignCluster, ok := ue.ObjectNew.(*liqov1beta1.ForeignCluster)
			if !ok {
				klog.Errorf("object %v is not a ForeignCluster", ue.ObjectNew)
				return
			}

			clusterID := newForeignCluster.Spec.ClusterID
			if clusterID == "" {
				klog.Errorf("cluster-id not set on foreignCluster %v", newForeignCluster)
				return
			}

			// List all shadowendpointslices with clusterID as origin
			var shadowList offloadingv1beta1.ShadowEndpointSliceList
			if err := r.List(ctx, &shadowList, client.MatchingLabels{forge.LiqoOriginClusterIDKey: string(clusterID)}); err != nil {
				klog.Errorf("Unable to list shadowendpointslices")
				return
			}

			for i := range shadowList.Items {
				shadow := &shadowList.Items[i]
				trli.Add(reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      shadow.Name,
						Namespace: shadow.Namespace,
					},
				})
			}
		},
	}
}

func (r *Reconciler) endpointsShouldBeUpdated(newObj, oldObj client.Object) bool {
	oldForeignCluster, ok := oldObj.(*liqov1beta1.ForeignCluster)
	if !ok {
		klog.Errorf("object %v is not a ForeignCluster", oldObj)
		return false
	}

	newForeignCluster, ok := newObj.(*liqov1beta1.ForeignCluster)
	if !ok {
		klog.Errorf("object %v is not a ForeignCluster", newObj)
		return false
	}

	oldFcNetworkReady := foreigncluster.IsNetworkingEstablishedOrDisabled(oldForeignCluster)
	newFcNetworkReady := foreigncluster.IsNetworkingEstablishedOrDisabled(newForeignCluster)

	oldFcAPIServerReady := foreigncluster.IsAPIServerReadyOrDisabled(oldForeignCluster)
	newFcAPIServerReady := foreigncluster.IsAPIServerReadyOrDisabled(newForeignCluster)

	// Reconcile if the network status or the API server status changed
	return oldFcNetworkReady != newFcNetworkReady || oldFcAPIServerReady != newFcAPIServerReady
}

// getDirectServiceNameForFailover checks whether the companion direct EndpointSlice for the
// given indirect ShadowEndpointSlice is in failover mode (DirectConnectionFailoverAnnotation == "true").
// When it is, the function returns the kubernetes.io/service-name value taken from the direct
// ShadowEPS (which always carries this label, kept up to date by the VK from the consumer EPS).
// Returns ("", nil) when failover is not active or the companion EPS / ShadowEPS is not found.
func (r *Reconciler) getDirectServiceNameForFailover(ctx context.Context, indirectShadowName, namespace string) (string, error) {
	if !strings.HasSuffix(indirectShadowName, forge.IndirectEndpointSliceSuffix) {
		return "", nil
	}
	directName := indirectShadowName[:len(indirectShadowName)-len(forge.IndirectEndpointSliceSuffix)]

	var directEPS discoveryv1.EndpointSlice
	if err := r.Get(ctx, types.NamespacedName{Name: directName, Namespace: namespace}, &directEPS); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	if directEPS.Annotations[consts.DirectConnectionFailoverAnnotation] != "true" {
		return "", nil
	}

	// The direct EPS already has its service-name label removed during failover; fetch the
	// direct ShadowEPS (maintained by VK, always has kubernetes.io/service-name) instead.
	var directShadow offloadingv1beta1.ShadowEndpointSlice
	if err := r.Get(ctx, types.NamespacedName{Name: directName, Namespace: namespace}, &directShadow); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	return directShadow.Labels[discoveryv1.LabelServiceName], nil
}

// getDirectEPSFailoverHandler returns an event handler that enqueues the companion indirect
// ShadowEndpointSlice for reconciliation whenever DirectConnectionFailoverAnnotation is set or
// removed on a direct EPS. This ensures the indirect EPS gains or loses kubernetes.io/service-name
// in sync with the direct EPS shedding or regaining it.
func (r *Reconciler) getDirectEPSFailoverHandler() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: types.NamespacedName{
				Name:      obj.GetName() + forge.IndirectEndpointSliceSuffix,
				Namespace: obj.GetNamespace(),
			},
		}}
	})
}

// SetupWithManager monitors updates on ShadowEndpointSlices.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, workers int) error {
	// Trigger a reconciliation only for Update Events on NetworkStatus of the ForeignCluster.
	fcPredicates := predicate.Funcs{
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		CreateFunc:  func(_ event.CreateEvent) bool { return false },
		UpdateFunc:  func(e event.UpdateEvent) bool { return r.endpointsShouldBeUpdated(e.ObjectNew, e.ObjectOld) },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}

	// Trigger the companion indirect ShadowEPS reconcile when DirectConnectionFailoverAnnotation
	// is added or removed on a direct EPS, so both EPS swap kubernetes.io/service-name together.
	directEPSFailoverPredicate := predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			lbls := e.ObjectNew.GetLabels()
			if lbls[forge.IndirectEndpointSliceLabelKey] == "true" {
				return false
			}
			if lbls[consts.ManagedByLabelKey] != consts.ManagedByShadowEndpointSliceValue {
				return false
			}
			oldA := e.ObjectOld.GetAnnotations()[consts.DirectConnectionFailoverAnnotation]
			newA := e.ObjectNew.GetAnnotations()[consts.DirectConnectionFailoverAnnotation]
			return oldA != newA
		},
		CreateFunc:  func(_ event.CreateEvent) bool { return false },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).Named(consts.CtrlShadowEndpointSlice).
		For(&offloadingv1beta1.ShadowEndpointSlice{}).
		Owns(&discoveryv1.EndpointSlice{}).
		Watches(&liqov1beta1.ForeignCluster{},
			r.getForeignClusterEventHandler(ctx), builder.WithPredicates(fcPredicates)).
		Watches(&discoveryv1.EndpointSlice{},
			r.getDirectEPSFailoverHandler(),
			builder.WithPredicates(directEPSFailoverPredicate)).
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}
