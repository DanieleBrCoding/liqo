package shadowendpointslicectrl

import (
	"context"
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	offloadingv1beta1 "github.com/liqotech/liqo/apis/offloading/v1beta1"
	"github.com/liqotech/liqo/pkg/consts"
)

func TestUseIndirectPath(t *testing.T) {
	for _, tc := range []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{"default direct", nil, false},
		{"latency preference", map[string]string{consts.LatencyPreferIndirectAnnotation: "true"}, true},
		{"network failover", map[string]string{consts.DirectConnectionFailoverAnnotation: "true"}, true},
		{"failover overrides direct preference", map[string]string{consts.DirectConnectionFailoverAnnotation: "true", consts.LatencyPreferIndirectAnnotation: "false"}, true},
		{"both", map[string]string{consts.DirectConnectionFailoverAnnotation: "true", consts.LatencyPreferIndirectAnnotation: "true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := useIndirectPath(tc.annotations); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIndirectCompanionFollowsLatencyPreference(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := discoveryv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := offloadingv1beta1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	direct := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Name: "server", Namespace: "demo",
		Annotations: map[string]string{consts.LatencyPreferIndirectAnnotation: "true"},
	}}
	shadow := &offloadingv1beta1.ShadowEndpointSlice{ObjectMeta: metav1.ObjectMeta{
		Name: "server", Namespace: "demo",
		Labels: map[string]string{discoveryv1.LabelServiceName: "demo-server"},
	}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(direct, shadow).Build()
	r := &Reconciler{Client: client}
	name, err := r.getDirectServiceNameForFailover(context.Background(), "server-indirect", "demo")
	if err != nil || name != "demo-server" {
		t.Fatalf("expected indirect companion to use demo-server, got %q: %v", name, err)
	}
	delete(direct.Annotations, consts.LatencyPreferIndirectAnnotation)
	if err := client.Update(context.Background(), direct); err != nil {
		t.Fatal(err)
	}
	name, err = r.getDirectServiceNameForFailover(context.Background(), "server-indirect", "demo")
	if err != nil || name != "" {
		t.Fatalf("expected inactive indirect companion, got %q: %v", name, err)
	}
}
