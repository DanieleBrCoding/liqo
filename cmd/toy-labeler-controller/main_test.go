package main

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func testPair() ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
	shadow := unstructured.Unstructured{Object: map[string]interface{}{
		"metadata": map[string]interface{}{
			"name": "server-1", "labels": map[string]interface{}{ServiceNameLabel: "demo-server"},
			"annotations": map[string]interface{}{directData: `{"clstrIDtoAddrs":{"provider2":["10.202.1.13"]}}`},
		},
		"spec": map[string]interface{}{"template": map[string]interface{}{
			"endpoints": []interface{}{map[string]interface{}{
				"addresses": []interface{}{"10.202.1.13"}, "targetRef": map[string]interface{}{"uid": "pod-uid"},
			}},
		}},
	}}
	ref := &corev1.ObjectReference{UID: types.UID("pod-uid")}
	direct := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "server-1", Namespace: "demo", Labels: map[string]string{ServiceNameLabel: "demo-server"}}, Endpoints: []discoveryv1.Endpoint{{TargetRef: ref, Addresses: []string{"10.202.1.13"}}}}
	indirect := discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "server-1-indirect", Namespace: "demo", Labels: map[string]string{IsIndirectLabel: "true"}}, Endpoints: []discoveryv1.Endpoint{{TargetRef: ref.DeepCopy(), Addresses: []string{"10.60.0.7"}}}}
	return []unstructured.Unstructured{shadow}, []discoveryv1.EndpointSlice{direct, indirect}
}

func TestSelectPair(t *testing.T) {
	shadows, slices := testPair()
	pair, err := selectPair(shadows, slices, "demo-server", "provider2")
	if err != nil || pair.directIP != "10.202.1.13" || pair.indirectIP != "10.60.0.7" {
		t.Fatalf("unexpected pair %#v, error %v", pair, err)
	}
	cases := []struct {
		name   string
		change func([]unstructured.Unstructured, []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice)
	}{
		{"missing indirect", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			return s, e[:1]
		}},
		{"different pods", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			e[1].Endpoints[0].TargetRef.UID = "other"
			return s, e
		}},
		{"identical addresses", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			e[1].Endpoints[0].Addresses[0] = e[0].Endpoints[0].Addresses[0]
			return s, e
		}},
		{"not ready", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			ready := false
			e[0].Endpoints[0].Conditions.Ready = &ready
			return s, e
		}},
		{"multiple endpoints", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			e[0].Endpoints = append(e[0].Endpoints, e[0].Endpoints[0])
			return s, e
		}},
		{"missing annotation", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			s[0].SetAnnotations(nil)
			return s, e
		}},
		{"wrong cluster", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			s[0].SetAnnotations(map[string]string{directData: `{"clstrIDtoAddrs":{"provider1":["10.1.1.1"]}}`})
			return s, e
		}},
		{"no service", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			s[0].SetLabels(nil)
			return s, e
		}},
		{"additional ready endpoint", func(s []unstructured.Unstructured, e []discoveryv1.EndpointSlice) ([]unstructured.Unstructured, []discoveryv1.EndpointSlice) {
			e = append(e, discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Name: "other", Labels: map[string]string{ServiceNameLabel: "demo-server"}}, Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.201.1.1"}}}})
			return s, e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, e := testPair()
			s, e = tc.change(s, e)
			if _, err := selectPair(s, e, "demo-server", "provider2"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestParsePingOutput(t *testing.T) {
	result, err := parsePingOutput("rtt min/avg/max/mdev = 0.500/0.800/1.200/0.100 ms")
	if err != nil || result.Avg != 0.8 {
		t.Fatalf("unexpected ping result: %#v, %v", result, err)
	}
	if _, err := parsePingOutput("rtt min/avg/max/mdev = 0.500/invalid/1.200/0.100 ms"); err == nil {
		t.Fatal("expected invalid ping statistic error")
	}
}

func TestSwitchPathHandlesErrorsAndDirectConfirmation(t *testing.T) {
	_, slices := testPair()
	client := fake.NewSimpleClientset(&slices[0], &slices[1])
	lc := &LatencyController{clientset: client, ctx: context.Background(), config: &ControllerConfig{Namespace: "demo", TargetService: "demo-server"}}
	pair := endpointPair{directName: "server-1", uid: "pod-uid", directIP: "10.202.1.13", indirectIP: "10.60.0.7"}
	if err := lc.switchPath(pair, false); err != nil {
		t.Fatalf("direct confirmation: %v", err)
	}

	pair.uid = "changed"
	if err := lc.switchPath(pair, true); err == nil {
		t.Fatal("expected stale endpoint error")
	}
	pair.uid = "pod-uid"
	client.PrependReactor("patch", "endpointslices", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("patch failed")
	})
	if err := lc.switchPath(pair, true); err == nil {
		t.Fatal("expected patch error")
	}
}

func TestSwitchPathIndirect(t *testing.T) {
	_, slices := testPair()
	client := fake.NewSimpleClientset(&slices[0], &slices[1])
	lc := &LatencyController{clientset: client, ctx: context.Background(), config: &ControllerConfig{Namespace: "demo", TargetService: "demo-server"}}
	pair := endpointPair{directName: "server-1", uid: "pod-uid", directIP: "10.202.1.13", indirectIP: "10.60.0.7"}
	client.PrependReactor("patch", "endpointslices", func(a ktesting.Action) (bool, runtime.Object, error) {
		direct := slices[0].DeepCopy()
		delete(direct.Labels, ServiceNameLabel)
		direct.Annotations = map[string]string{latencyPreference: "true"}
		indirect := slices[1].DeepCopy()
		indirect.Labels[ServiceNameLabel] = "demo-server"
		if err := client.Tracker().Update(discoveryv1.SchemeGroupVersion.WithResource("endpointslices"), direct, "demo"); err != nil {
			return true, nil, err
		}
		if err := client.Tracker().Update(discoveryv1.SchemeGroupVersion.WithResource("endpointslices"), indirect, "demo"); err != nil {
			return true, nil, err
		}
		return true, direct, nil
	})
	if err := lc.switchPath(pair, true); err != nil {
		t.Fatalf("indirect confirmation: %v", err)
	}
}

func TestSwitchPathDoesNotConfirmDirectDuringNetworkFailover(t *testing.T) {
	_, slices := testPair()
	slices[0].Annotations = map[string]string{networkFailover: "true"}
	client := fake.NewSimpleClientset(&slices[0], &slices[1])
	lc := &LatencyController{clientset: client, ctx: context.Background(), config: &ControllerConfig{Namespace: "demo", TargetService: "demo-server"}}
	pair := endpointPair{directName: "server-1", uid: "pod-uid", directIP: "10.202.1.13", indirectIP: "10.60.0.7"}
	if err := lc.switchPath(pair, false); err == nil {
		t.Fatal("expected network failover error")
	}
}
