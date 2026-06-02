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

// Package connectionfailoverctrl implements a controller that monitors Connection CRs and
// signals failover state on direct-connection EndpointSlices via an annotation so that
// the ShadowEndpointSlice controller can swap kube-proxy traffic to the hub-and-spoke path.
package connectionfailoverctrl

import (
	"context"
	"encoding/json"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	networkingv1beta1 "github.com/liqotech/liqo/apis/networking/v1beta1"
	offloadingv1beta1 "github.com/liqotech/liqo/apis/offloading/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
	directconnectioninfo "github.com/liqotech/liqo/pkg/utils/directconnection"
	"github.com/liqotech/liqo/pkg/virtualKubelet/forge"
)

const (
	ctrlFieldManager = "connection-failover-controller"
)

// Reconciler watches Connection CRs and patches the provider-side EndpointSlices
// so that traffic falls back to hub-and-spoke addresses when the direct tunnel is down.
type Reconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=networking.liqo.io,resources=connections,verbs=get;list;watch
// +kubebuilder:rbac:groups=networking.liqo.io,resources=connections/status,verbs=get;list;watch
// +kubebuilder:rbac:groups=offloading.liqo.io,resources=shadowendpointslices,verbs=get;list;watch
// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;list;watch;update;patch

// Reconcile reacts to a Connection change and ensures the failover state of related EndpointSlices
// matches the current connection status.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	nsName := req.NamespacedName
	klog.V(4).Infof("connection-failover: reconcile connection %q", nsName)

	var conn networkingv1beta1.Connection
	if err := r.Get(ctx, nsName, &conn); err != nil {
		if errors.IsNotFound(err) {
			// Connection deleted.
			// The DeleteFunc predicate is set to false, so this branch is reached only when the
			// object disappears between the predicate evaluation and the Get (very rare).
			// Either way there is nothing actionable: the failover annotation is intentionally left
			// in place on any affected EPS so that hub-and-spoke addresses remain active while the
			// tunnel is gone. The annotation will be removed once a new Connection CR is created
			// and reaches the Connected state (CreateFunc:true fires the reconcile).
			klog.V(4).Infof("connection-failover: connection %q not found, nothing to do", nsName)
			return ctrl.Result{}, nil
		}
		klog.Errorf("connection-failover: error getting connection %q: %v", nsName, err)
		return ctrl.Result{}, err
	}

	remoteClusterID := conn.Labels[consts.RemoteClusterID]
	if remoteClusterID == "" {
		klog.V(4).Infof("connection-failover: connection %q has no %q label, skipping", nsName, consts.RemoteClusterID)
		return ctrl.Result{}, nil
	}

	// Connecting is a transient state: the tunnel is being (re-)established. Do nothing and
	// wait for either Connected or Error. Activating failover during Connecting would reroute
	// traffic unnecessarily during normal WireGuard re-keying or gateway restarts.
	if conn.Status.Value == networkingv1beta1.Connecting {
		klog.V(4).Infof("connection-failover: connection %q is in Connecting state, skipping", nsName)
		return ctrl.Result{}, nil
	}

	connectionUp := conn.Status.Value == networkingv1beta1.Connected

	klog.V(4).Infof("connection-failover: connection %q to cluster %q, up=%v", nsName, remoteClusterID, connectionUp)

	var allShadowList offloadingv1beta1.ShadowEndpointSliceList
	if err := r.List(ctx, &allShadowList); err != nil {
		klog.Errorf("connection-failover: error listing ShadowEndpointSlices: %v", err)
		return ctrl.Result{}, err
	}

	// Find the ShadowEndpointSlices that are relevant for this Connection by checking the DirectConnectionData annotation
	for i := range allShadowList.Items {
		shadow := &allShadowList.Items[i]

		// Skip indirect ShadowEPS — they carry hub-and-spoke addresses and are the failover source.
		if shadow.Labels[forge.IndirectEndpointSliceLabelKey] == "true" {
			continue
		}

		// Skip ShadowEPS that do not carry direct connection data at all.
		annotationVal, hasAnnotation := shadow.Annotations[consts.DirectConnectionDataAnnotationKey]
		if !hasAnnotation {
			continue
		}

		// Check whether this ShadowEPS references the failing/recovering cluster found in the Connection.
		if !directConnectionDataContainsCluster(annotationVal, remoteClusterID) {
			continue
		}

		if err := r.reconcileFailoverForEPS(ctx, shadow.Name, shadow.Namespace, connectionUp); err != nil {
			// Log and continue — other EPS should still be processed.
			klog.Errorf("connection-failover: error reconciling EPS %s/%s: %v", shadow.Namespace, shadow.Name, err)
		}
	}

	return ctrl.Result{}, nil
}

// reconcileFailoverForEPS sets or removes DirectConnectionFailoverAnnotation on the direct EPS.
// The annotation is the signal the ShadowEPS controller observes: it swaps which EPS carries
// kubernetes.io/service-name, steering kube-proxy to the hub-and-spoke path without freezing
// or copying endpoints (both EPS continue to be updated normally).
func (r *Reconciler) reconcileFailoverForEPS(ctx context.Context, name, namespace string, connectionUp bool) error {
	var directEPS discoveryv1.EndpointSlice
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &directEPS); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	alreadyInFailover := directEPS.Annotations[consts.DirectConnectionFailoverAnnotation] == "true"

	if connectionUp {
		if !alreadyInFailover {
			return nil
		}
		// Restore: remove the failover annotation so the ShadowEPS controller reconciles normally.
		klog.Infof("connection-failover: restoring direct EPS %s/%s (connection is up)", namespace, name)
		patch := client.MergeFrom(directEPS.DeepCopy())
		delete(directEPS.Annotations, consts.DirectConnectionFailoverAnnotation)
		if err := r.Patch(ctx, &directEPS, patch, client.FieldOwner(ctrlFieldManager)); err != nil {
			klog.Errorf("connection-failover: failed to remove failover annotation from EPS %s/%s: %v", namespace, name, err)
			return err
		}
		klog.Infof("connection-failover: direct EPS %s/%s restored to direct path", namespace, name)
		return nil
	}

	// Connection is down.
	if alreadyInFailover {
		klog.V(4).Infof("connection-failover: EPS %s/%s already in failover mode", namespace, name)
		return nil
	}

	klog.Infof("connection-failover: activating failover for direct EPS %s/%s", namespace, name)
	snapshot := client.MergeFrom(directEPS.DeepCopy())
	if directEPS.Annotations == nil {
		directEPS.Annotations = make(map[string]string)
	}
	directEPS.Annotations[consts.DirectConnectionFailoverAnnotation] = "true"
	// Explicitly remove kubernetes.io/service-name in the same non-SSA patch.
	// The EPS was created via a non-SSA r.Create call whose managed-fields record
	// (operation="Update") retains ownership of the label.  An SSA Apply that omits
	// the label only removes it from the "Apply" record; the "Update" record keeps
	// the field alive in the object.  Deleting it here (non-SSA MergeFrom) removes
	// it from the live object regardless of managed-field ownership.
	delete(directEPS.Labels, discoveryv1.LabelServiceName)
	if err := r.Patch(ctx, &directEPS, snapshot, client.FieldOwner(ctrlFieldManager)); err != nil {
		klog.Errorf("connection-failover: failed to set failover annotation on EPS %s/%s: %v", namespace, name, err)
		return err
	}
	klog.Infof("connection-failover: direct EPS %s/%s is now in failover mode", namespace, name)
	return nil
}

// directConnectionDataContainsCluster checks whether the JSON-encoded DirectConnectionData
// annotation value references the given clusterID as a key.
func directConnectionDataContainsCluster(annotationVal, clusterID string) bool {
	var data directconnectioninfo.DirectConnectionData
	if err := json.Unmarshal([]byte(annotationVal), &data); err != nil {
		return false
	}
	_, ok := data.ByCluster[clusterID]
	return ok
}

// getDirectEPSCreateEventHandler returns an event handler for newly created direct EndpointSlices.
// When the ShadowEPS controller creates a new direct EPS (e.g. after a VK reconnect that caused
// the ShadowEPS to be deleted and recreated), failover may need to be re-activated immediately if
// the relevant Connection is already down. This handler enqueues reconcile requests for all
// Connections whose remote cluster ID appears in the corresponding ShadowEPS annotation.
func (r *Reconciler) getDirectEPSCreateEventHandler(ctx context.Context) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
		eps, ok := obj.(*discoveryv1.EndpointSlice)
		if !ok {
			return nil
		}

		// Look up the ShadowEPS (same name/namespace) to check for DirectConnectionDataAnnotationKey.
		var shadowEPS offloadingv1beta1.ShadowEndpointSlice
		if err := r.Get(ctx, types.NamespacedName{Name: eps.Name, Namespace: eps.Namespace}, &shadowEPS); err != nil {
			return nil
		}

		annotationVal, ok := shadowEPS.Annotations[consts.DirectConnectionDataAnnotationKey]
		if !ok {
			// Not a direct-connection EPS — nothing to do.
			return nil
		}

		var data directconnectioninfo.DirectConnectionData
		if err := json.Unmarshal([]byte(annotationVal), &data); err != nil {
			return nil
		}

		// Enqueue a reconcile request for each Connection that references one of the peer cluster IDs.
		var requests []reconcile.Request
		var connList networkingv1beta1.ConnectionList
		if err := r.List(ctx, &connList); err != nil {
			return nil
		}
		for i := range connList.Items {
			conn := &connList.Items[i]
			peerID := conn.Labels[consts.RemoteClusterID]
			if _, involved := data.ByCluster[peerID]; involved {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      conn.Name,
						Namespace: conn.Namespace,
					},
				})
			}
		}
		return requests
	})
}

// SetupWithManager registers the controller watches.
func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, workers int) error {
	// Predicate for Connection: only react when Status.Value changes or on delete.
	connectionPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Reconcile on create to handle controller restarts while a connection is already down.
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldConn, ok1 := e.ObjectOld.(*networkingv1beta1.Connection)
			newConn, ok2 := e.ObjectNew.(*networkingv1beta1.Connection)
			if !ok1 || !ok2 {
				return false
			}
			// Only act on transitions that involve Connected or Error:
			// - Any → Connected (restore)
			// - Connected → Error (failover)
			// - Connecting → Error (failover; tunnel did not come up)
			// Connecting is skipped in Reconcile itself, so Connecting→Connecting or
			// Error→Connecting transitions need not fire a reconcile.
			if oldConn.Status.Value == newConn.Status.Value {
				return false
			}
			// Skip transitions where neither old nor new state is Connected or Error.
			if oldConn.Status.Value == networkingv1beta1.Connecting &&
				newConn.Status.Value == networkingv1beta1.Connecting {
				return false
			}
			return true
		},
		// DeleteFunc is false: when the Connection is deleted we intentionally leave the failover
		// annotation on affected EPS so hub-and-spoke addresses stay active while the tunnel is
		// absent. The annotation is cleared when the Connection is re-created and becomes Connected.
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}

	// Predicate for direct EPS creation: only react to create events on non-indirect EPS managed
	// by the ShadowEPS controller. This covers the case where a direct EPS is recreated while a
	// Connection is already down (e.g. VK reconnect causes ShadowEPS delete+recreate): the new
	// EPS has no failover annotation, and we need to re-activate failover immediately.
	directEPSCreatePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			labels := e.Object.GetLabels()
			return labels[consts.ManagedByLabelKey] == consts.ManagedByShadowEndpointSliceValue &&
				labels[forge.IndirectEndpointSliceLabelKey] != "true"
		},
		UpdateFunc:  func(_ event.UpdateEvent) bool { return false },
		DeleteFunc:  func(_ event.DeleteEvent) bool { return false },
		GenericFunc: func(_ event.GenericEvent) bool { return false },
	}

	return ctrl.NewControllerManagedBy(mgr).Named(consts.CtrlConnectionFailover).
		For(&networkingv1beta1.Connection{}, builder.WithPredicates(connectionPredicate)).
		Watches(
			&discoveryv1.EndpointSlice{},
			r.getDirectEPSCreateEventHandler(ctx),
			builder.WithPredicates(directEPSCreatePredicate),
		).
		WithOptions(controller.Options{MaxConcurrentReconciles: workers}).
		Complete(r)
}
