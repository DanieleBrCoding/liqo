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

// ─── HOW THESE TESTS ARE ORGANISED ──────────────────────────────────────────
//
// # BDD (Behaviour Driven Development) with Ginkgo
//
// Ginkgo lets you nest containers to describe *context* and leaf nodes to make
// *assertions*:
//
//	Describe("thing under test") {
//	  When("some precondition") {
//	    BeforeEach(func() { /* set up state */ })
//	    It("does the right thing") { /* assert */ }
//	    It("also does this") { /* another assert for the same state */ }
//	  }
//	  When("a different precondition") { … }
//	}
//
// # BeforeEach vs JustBeforeEach
//
// Both run before every It(), but in different order:
//
//	BeforeEach  → runs from outermost to innermost, used to BUILD test state
//	JustBeforeEach → runs after all BeforeEach blocks, used to EXECUTE the action
//
// This split lets inner BeforeEach blocks change the fakeClient (or any variable)
// *before* we call Reconcile(), without duplicating the Reconcile call.
//
// # Fake client
//
// Instead of a real Kubernetes API server we use the controller-runtime fake client:
//
//	fakeClient = fake.NewClientBuilder().
//	    WithScheme(scheme.Scheme).   // so it knows about our CRDs
//	    WithObjects(obj1, obj2).     // pre-populate the in-memory store
//	    Build()
//
// The fake client supports Get/List/Create/Update/Delete and strategic-merge-patch
// (MergeFrom), which is all the failover controller needs.
//
// Note: server-side apply (ApplyPatchType) is NOT supported by the fake client;
// the ShadowEPS controller uses SSA, so its tests skip on that error — see the
// existing shadowendpointslice_controller_test.go.  The failover controller only
// uses MergeFrom, so it works without any workaround.
//
// # Gomega matchers used here
//
//	Expect(expr).To(Succeed())              — expr returns (_, error): error must be nil
//	Expect(expr).To(HaveKeyWithValue(k, v)) — map contains key k with value v
//	Expect(expr).NotTo(HaveKey(k))          — map does NOT contain key k
//	Expect(expr).To(BeEmpty())              — length is 0 / string is ""
//	Expect(err).NotTo(HaveOccurred())       — convenience alias for error == nil
// ─────────────────────────────────────────────────────────────────────────────

package connectionfailoverctrl

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	networkingv1beta1 "github.com/liqotech/liqo/apis/networking/v1beta1"
	offloadingv1beta1 "github.com/liqotech/liqo/apis/offloading/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
	"github.com/liqotech/liqo/pkg/virtualKubelet/forge"
)

// ─── shared constants ────────────────────────────────────────────────────────

const (
	testNamespace = "default"
	testConnName  = "test-connection"
	testClusterID = "test-cluster-id"
	testEPSName   = "test-eps"
	testSvcName   = "my-service"

	// JSON value for consts.DirectConnectionDataAnnotationKey that references testClusterID.
	// The struct is DirectConnectionData{ByCluster: map[string][]string{testClusterID: {"10.0.0.1"}}}.
	// The JSON key ("clstrIDtoAddrs") comes from the struct tag in directconnection.go.
	directConnAnnotation = `{"clstrIDtoAddrs":{"` + testClusterID + `":["10.0.0.1"]}}`

	// JSON annotation for a *different* cluster that should not trigger failover.
	otherClusterAnnotation = `{"clstrIDtoAddrs":{"other-cluster":["10.0.0.2"]}}`
)

// ─── object factory helpers ──────────────────────────────────────────────────
//
// Factory functions are small closures that build Kubernetes objects for a test.
// Keeping them as functions (rather than package-level vars) avoids accidental
// state sharing between specs.

// newConnection builds a Connection CR in testNamespace.
//   - status: the Status.Value to set (Connected / Connecting / ConnectionError)
//   - withClusterLabel: if true, adds the liqo.io/remote-cluster-id label that the
//     controller reads to identify which cluster the Connection belongs to.
func newConnection(status networkingv1beta1.ConnectionStatusValue, withClusterLabel bool) *networkingv1beta1.Connection {
	conn := &networkingv1beta1.Connection{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testConnName,
			Namespace: testNamespace,
		},
		Status: networkingv1beta1.ConnectionStatus{
			Value: status,
		},
	}
	if withClusterLabel {
		conn.Labels = map[string]string{
			consts.RemoteClusterID: testClusterID,
		}
	}
	return conn
}

// newDirectShadowEPS builds a direct (non-indirect) ShadowEndpointSlice that
// references testClusterID via consts.DirectConnectionDataAnnotationKey.
// This is the object the controller Lists to find which EPS to patch.
func newDirectShadowEPS() *offloadingv1beta1.ShadowEndpointSlice {
	return &offloadingv1beta1.ShadowEndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEPSName,
			Namespace: testNamespace,
			Labels: map[string]string{
				consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue,
				// Note: IndirectEndpointSliceLabelKey is intentionally absent — this is a *direct* ShadowEPS.
			},
			Annotations: map[string]string{
				consts.DirectConnectionDataAnnotationKey: directConnAnnotation,
			},
		},
	}
}

// newIndirectShadowEPS builds an indirect ShadowEndpointSlice.
// The controller must skip it even when it references testClusterID.
func newIndirectShadowEPS() *offloadingv1beta1.ShadowEndpointSlice {
	shadow := newDirectShadowEPS()
	shadow.Labels[forge.IndirectEndpointSliceLabelKey] = "true"
	return shadow
}

// newDirectEPS builds the provider-side EndpointSlice (same name as the ShadowEPS).
//   - withServiceName:       pre-populate kubernetes.io/service-name (normal state before failover)
//   - withFailoverAnnotation: pre-set consts.DirectConnectionFailoverAnnotation (simulates a previous
//     failover that is still in effect)
func newDirectEPS(withServiceName, withFailoverAnnotation bool) *discoveryv1.EndpointSlice {
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testEPSName,
			Namespace: testNamespace,
			Labels: map[string]string{
				consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue,
			},
		},
	}
	if withServiceName {
		eps.Labels[discoveryv1.LabelServiceName] = testSvcName
	}
	if withFailoverAnnotation {
		eps.Annotations = map[string]string{
			consts.DirectConnectionFailoverAnnotation: "true",
		}
	}
	return eps
}

// ─── specs ───────────────────────────────────────────────────────────────────

var _ = Describe("ConnectionFailover Reconciler", func() {
	// These variables are re-assigned in BeforeEach blocks; they are declared here
	// so that all nested containers and JustBeforeEach share them.
	var (
		ctx        context.Context
		fakeClient client.WithWatch
		err        error

		// The Reconcile request always targets the Connection resource.
		req = ctrl.Request{
			NamespacedName: types.NamespacedName{
				Name:      testConnName,
				Namespace: testNamespace,
			},
		}
	)

	// Outer BeforeEach: runs before every It() unless overridden by an inner one.
	// Here we just initialise ctx; fakeClient is set by each nested BeforeEach.
	BeforeEach(func() {
		ctx = context.TODO()
	})

	// JustBeforeEach runs after ALL BeforeEach blocks (inner and outer) have finished.
	// We place the actual reconcile call here so that every inner BeforeEach can
	// configure fakeClient (and thus the world the reconciler sees) without having
	// to repeat the reconcile invocation.
	JustBeforeEach(func() {
		r := &Reconciler{
			Client: fakeClient,
			Scheme: scheme.Scheme,
		}
		_, err = r.Reconcile(ctx, req)
		Expect(err).NotTo(HaveOccurred())
	})

	// ── 1. Connection not found ──────────────────────────────────────────────
	// The controller should gracefully ignore a missing Connection (it may have
	// been deleted between the event firing and the reconcile running).
	When("the Connection resource does not exist", func() {
		BeforeEach(func() {
			// Empty fake store — no objects at all.
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
		})

		It("should return without error", func() {
			// err is already checked in JustBeforeEach; this It block is kept explicit
			// so the test report has a meaningful description.
			Expect(err).NotTo(HaveOccurred())
		})
	})

	// ── 2. Connection has no cluster label ──────────────────────────────────
	// The controller reads conn.Labels[consts.RemoteClusterID] to know which
	// cluster the Connection belongs to. When the label is absent it cannot match
	// any ShadowEPS, so it logs and returns.
	When("the Connection has no RemoteClusterID label", func() {
		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.ConnectionError, false /* withClusterLabel */)
			directEPS := newDirectEPS(true, false)
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
		})

		It("should not patch the EndpointSlice", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			// Annotation must still be absent — controller did nothing.
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
		})
	})

	// ── 3. Connection is Connecting (transient) ──────────────────────────────
	// While the tunnel is being (re-)established the controller deliberately does
	// nothing to avoid short-lived flaps during normal WireGuard re-keying.
	When("the Connection is in Connecting state", func() {
		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.Connecting, true)
			directEPS := newDirectEPS(true, false)
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
		})

		It("should not set the failover annotation on the EndpointSlice", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
		})

		It("should preserve kubernetes.io/service-name", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, testSvcName))
		})
	})

	// ── 4. Connection is Error (failover activation path) ────────────────────
	When("the Connection is Error (tunnel down)", func() {

		// ── 4a. No failover annotation yet — the normal activation case ──────
		When("the direct EPS has no failover annotation yet", func() {
			BeforeEach(func() {
				conn := newConnection(networkingv1beta1.ConnectionError, true)
				directEPS := newDirectEPS(true /* withServiceName */, false /* withFailoverAnnotation */)
				fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
					WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
			})

			// This is the primary assertion for the feature: failover annotation set.
			It("should set the failover annotation on the direct EPS", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Annotations).To(HaveKeyWithValue(consts.DirectConnectionFailoverAnnotation, "true"))
			})

			// ────────────────────────────────────────────────────────────────
			// BUG FIX TEST
			//
			// When the EPS is first created via a non-SSA r.Create call, the
			// API server records kubernetes.io/service-name in a managed-fields
			// entry with operation="Update". A later SSA Apply that omits the
			// label only removes it from the operation="Apply" record; the
			// "Update" record retains ownership and the label stays in the
			// object. The fix: the failover controller deletes the label in the
			// same MergeFrom patch that sets the annotation, bypassing managed-
			// field ownership entirely.
			// ────────────────────────────────────────────────────────────────
			It("should remove kubernetes.io/service-name from the direct EPS", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Labels).NotTo(HaveKey(discoveryv1.LabelServiceName))
			})
		})

		// ── 4b. Already in failover — idempotency ────────────────────────────
		// The controller must not error or produce redundant patches when the
		// annotation is already present (e.g. controller restarted mid-failover).
		When("the direct EPS already has the failover annotation", func() {
			BeforeEach(func() {
				conn := newConnection(networkingv1beta1.ConnectionError, true)
				// EPS has no service-name (already removed in a previous reconcile) and
				// already carries the failover annotation.
				directEPS := newDirectEPS(false /* withServiceName */, true /* withFailoverAnnotation */)
				fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
					WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
			})

			It("should keep the failover annotation (idempotent)", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Annotations).To(HaveKeyWithValue(consts.DirectConnectionFailoverAnnotation, "true"))
			})
		})
	})

	// ── 5. Connection is Connected (failover restoration path) ───────────────
	When("the Connection is Connected (tunnel up)", func() {

		// ── 5a. EPS is in failover — restore it ──────────────────────────────
		When("the direct EPS has the failover annotation", func() {
			BeforeEach(func() {
				conn := newConnection(networkingv1beta1.Connected, true)
				// EPS is in failover state: no service-name, annotation present.
				directEPS := newDirectEPS(false /* withServiceName */, true /* withFailoverAnnotation */)
				fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
					WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
			})

			It("should remove the failover annotation", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
			})

			// The failover controller intentionally does NOT restore kubernetes.io/service-name
			// here. That is the ShadowEPS controller's responsibility: once the annotation is
			// gone, its next SSA Apply will include the label again.
			It("should not restore kubernetes.io/service-name (that is the ShadowEPS controller's job)", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Labels).NotTo(HaveKey(discoveryv1.LabelServiceName))
			})
		})

		// ── 5b. EPS is already healthy — no-op ───────────────────────────────
		When("the direct EPS has no failover annotation", func() {
			BeforeEach(func() {
				conn := newConnection(networkingv1beta1.Connected, true)
				directEPS := newDirectEPS(true /* withServiceName */, false /* withFailoverAnnotation */)
				fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
					WithObjects(conn, newDirectShadowEPS(), directEPS).Build()
			})

			It("should not modify the EndpointSlice", func() {
				eps := &discoveryv1.EndpointSlice{}
				Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
				Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, testSvcName))
				Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
			})
		})
	})

	// ── 6. Indirect ShadowEPS must be skipped ───────────────────────────────
	// Indirect ShadowEPS carry hub-and-spoke addresses. The controller must never
	// annotate the EPS corresponding to an indirect ShadowEPS.
	When("the only ShadowEPS for the cluster is indirect", func() {
		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.ConnectionError, true)
			// Use indirect ShadowEPS even though it carries the cluster annotation.
			directEPS := newDirectEPS(true, false)
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, newIndirectShadowEPS(), directEPS).Build()
		})

		It("should not set the failover annotation on the EPS", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
		})

		It("should preserve kubernetes.io/service-name", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, testSvcName))
		})
	})

	// ── 7. ShadowEPS without DirectConnectionDataAnnotationKey ──────────────
	// A ShadowEPS that does not carry the direct-connection annotation is a normal
	// (non-direct) ShadowEPS and must be silently skipped.
	When("the ShadowEPS has no DirectConnectionDataAnnotationKey annotation", func() {
		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.ConnectionError, true)
			shadowWithoutAnnotation := &offloadingv1beta1.ShadowEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testEPSName,
					Namespace: testNamespace,
					Labels: map[string]string{
						consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue,
					},
					// Note: no DirectConnectionDataAnnotationKey here.
				},
			}
			directEPS := newDirectEPS(true, false)
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, shadowWithoutAnnotation, directEPS).Build()
		})

		It("should not modify the EndpointSlice", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
			Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, testSvcName))
		})
	})

	// ── 8. ShadowEPS references a different cluster ──────────────────────────
	// The annotation on the ShadowEPS lists "other-cluster", not testClusterID.
	// The controller should not touch this EPS.
	When("the ShadowEPS annotation references a different cluster", func() {
		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.ConnectionError, true)
			shadowOtherCluster := &offloadingv1beta1.ShadowEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      testEPSName,
					Namespace: testNamespace,
					Labels: map[string]string{
						consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue,
					},
					Annotations: map[string]string{
						consts.DirectConnectionDataAnnotationKey: otherClusterAnnotation,
					},
				},
			}
			directEPS := newDirectEPS(true, false)
			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, shadowOtherCluster, directEPS).Build()
		})

		It("should not modify the EndpointSlice", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
			Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, testSvcName))
		})
	})

	// ── 9. Multiple ShadowEPS — only matching one is patched ────────────────
	// When there are two direct ShadowEPS (different clusters), only the one that
	// references the failing cluster should have its EPS patched.
	When("multiple ShadowEPS exist for different clusters", func() {
		const (
			otherEPSName = "other-eps"
			otherSvcName = "other-service"
			otherCluster = "other-cluster"
		)

		BeforeEach(func() {
			conn := newConnection(networkingv1beta1.ConnectionError, true) // testClusterID is down

			// ShadowEPS / EPS for testClusterID — should be patched.
			failingShadow := newDirectShadowEPS()
			failingEPS := newDirectEPS(true, false)

			// ShadowEPS / EPS for a different cluster — must remain untouched.
			healthyShadow := &offloadingv1beta1.ShadowEndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      otherEPSName,
					Namespace: testNamespace,
					Labels: map[string]string{
						consts.ManagedByLabelKey: consts.ManagedByShadowEndpointSliceValue,
					},
					Annotations: map[string]string{
						consts.DirectConnectionDataAnnotationKey: `{"clstrIDtoAddrs":{"` + otherCluster + `":["10.0.0.2"]}}`,
					},
				},
			}
			healthyEPS := &discoveryv1.EndpointSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name:      otherEPSName,
					Namespace: testNamespace,
					Labels: map[string]string{
						consts.ManagedByLabelKey:     consts.ManagedByShadowEndpointSliceValue,
						discoveryv1.LabelServiceName: otherSvcName,
					},
				},
			}

			fakeClient = fake.NewClientBuilder().WithScheme(scheme.Scheme).
				WithObjects(conn, failingShadow, failingEPS, healthyShadow, healthyEPS).Build()
		})

		It("should set the failover annotation on the failing cluster's EPS", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: testEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).To(HaveKeyWithValue(consts.DirectConnectionFailoverAnnotation, "true"))
			Expect(eps.Labels).NotTo(HaveKey(discoveryv1.LabelServiceName))
		})

		It("should not touch the healthy cluster's EPS", func() {
			eps := &discoveryv1.EndpointSlice{}
			Expect(fakeClient.Get(ctx, types.NamespacedName{Name: otherEPSName, Namespace: testNamespace}, eps)).To(Succeed())
			Expect(eps.Annotations).NotTo(HaveKey(consts.DirectConnectionFailoverAnnotation))
			Expect(eps.Labels).To(HaveKeyWithValue(discoveryv1.LabelServiceName, otherSvcName))
		})
	})
})

// ─── unit tests for directConnectionDataContainsCluster ──────────────────────
//
// This is a pure function (no I/O, no state), so we can test it directly without
// needing a fake client or a Reconciler.  Using Ginkgo here keeps the test style
// consistent with the rest of the package.

var _ = Describe("directConnectionDataContainsCluster", func() {
	It("returns true when the annotation JSON contains the cluster ID", func() {
		Expect(directConnectionDataContainsCluster(directConnAnnotation, testClusterID)).To(BeTrue())
	})

	It("returns false when the annotation JSON does not contain the cluster ID", func() {
		Expect(directConnectionDataContainsCluster(directConnAnnotation, "missing-cluster")).To(BeFalse())
	})

	It("returns false when the JSON is empty / invalid", func() {
		Expect(directConnectionDataContainsCluster("", testClusterID)).To(BeFalse())
		Expect(directConnectionDataContainsCluster("{invalid json}", testClusterID)).To(BeFalse())
	})

	It("returns false when the JSON object is valid but ByCluster is empty", func() {
		Expect(directConnectionDataContainsCluster(`{"clstrIDtoAddrs":{}}`, testClusterID)).To(BeFalse())
	})
})
