package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// Configuration constants
	DefaultNamespace        = "hub-spoke-demo"
	DefaultLatencyThreshold = 2.0 // percent
	DefaultMeasureInterval  = 30  // seconds - how often to measure
	PingCount               = 5   // number of ping requests per measurement

	// Labels for identifying EndpointSlices
	ServiceNameLabel  = "kubernetes.io/service-name"
	IsIndirectLabel   = "liqo.io/indirect-endpointslice"
	latencyPreference = "liqo.io/latency-prefer-indirect"
	networkFailover   = "liqo.io/direct-connection-failover"
	directData        = "liqo.io/direct-connections-data"
)

var shadowResource = schema.GroupVersionResource{Group: "offloading.liqo.io", Version: "v1beta1", Resource: "shadowendpointslices"}

type LatencyMetrics struct {
	Min    float64
	Avg    float64
	Max    float64
	StdDev float64
}

type ControllerConfig struct {
	Namespace        string
	LatencyThreshold float64
	MeasureInterval  time.Duration
	PingCount        int
	TargetService    string // The service to switch (e.g., "demo-server")
	TargetCluster    string
}

type LatencyController struct {
	config    *ControllerConfig
	clientset kubernetes.Interface
	dynamic   dynamic.Interface
	ctx       context.Context
}

// NewLatencyController creates a new latency controller
func NewLatencyController(config *ControllerConfig) (*LatencyController, error) {
	// Get in-cluster config
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		klog.Errorf("Failed to get in-cluster config: %v", err)
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(inClusterConfig)
	if err != nil {
		klog.Errorf("Failed to create clientset: %v", err)
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(inClusterConfig)
	if err != nil {
		return nil, fmt.Errorf("create Liqo client: %w", err)
	}

	return &LatencyController{
		config:    config,
		clientset: clientset,
		dynamic:   dynamicClient,
		ctx:       context.Background(),
	}, nil
}

// Run starts the main controller loop
func (lc *LatencyController) Run(stopCh <-chan struct{}) {
	klog.Infof("Starting Latency Controller")
	klog.Infof("  Namespace: %s", lc.config.Namespace)
	klog.Infof("  Target Service: %s", lc.config.TargetService)
	klog.Infof("  Target Cluster: %s", lc.config.TargetCluster)
	klog.Infof("  Latency Threshold: %.2f%%", lc.config.LatencyThreshold)
	klog.Infof("  Measure Interval: %s", lc.config.MeasureInterval)

	ticker := time.NewTicker(lc.config.MeasureInterval)
	defer ticker.Stop()

	// Run measurement immediately on startup
	lc.measure()

	for {
		select {
		case <-stopCh:
			klog.Infof("Stopping Latency Controller")
			return
		case <-ticker.C:
			lc.measure()
		}
	}
}

// measure performs a single measurement cycle
func (lc *LatencyController) measure() {
	pair, err := lc.findPair()
	if err != nil {
		klog.Errorf("Cannot select a remote endpoint pair: %v", err)
		return
	}
	klog.Infof("Measuring %s on %s: direct=%s indirect=%s", pair.uid, lc.config.TargetCluster, pair.directIP, pair.indirectIP)
	directMetrics, err := lc.measureLatency(pair.directIP)
	if err != nil {
		klog.Errorf("Failed to measure direct path latency: %v", err)
		return
	}

	indirectMetrics, err := lc.measureLatency(pair.indirectIP)
	if err != nil {
		klog.Errorf("Failed to measure indirect path latency: %v", err)
		return
	}

	// Log results
	klog.Infof("Latency Results (avg):")
	klog.Infof("  Direct:   %.2f ms (min=%.2f, max=%.2f)", directMetrics.Avg, directMetrics.Min, directMetrics.Max)
	klog.Infof("  Indirect: %.2f ms (min=%.2f, max=%.2f)", indirectMetrics.Avg, indirectMetrics.Min, indirectMetrics.Max)

	current, err := lc.findPair()
	if err != nil || current != pair {
		klog.Errorf("Endpoint pair changed during measurement (before=%+v, after=%+v, error=%v); not switching", pair, current, err)
		return
	}
	lc.decideAndSwitch(pair, directMetrics, indirectMetrics)
}

type endpointPair struct {
	directName           string
	uid                  types.UID
	directIP, indirectIP string
}

func selectPair(shadows []unstructured.Unstructured, slices []discoveryv1.EndpointSlice, service, cluster string) (endpointPair, error) {
	byName := make(map[string]discoveryv1.EndpointSlice, len(slices))
	for _, slice := range slices {
		byName[slice.Name] = slice
	}
	var pairs []endpointPair
	for _, shadow := range shadows {
		if shadow.GetLabels()[ServiceNameLabel] != service || shadow.GetLabels()[IsIndirectLabel] == "true" {
			continue
		}
		var data struct {
			ByCluster map[string][]string `json:"clstrIDtoAddrs"`
		}
		if err := json.Unmarshal([]byte(shadow.GetAnnotations()[directData]), &data); err != nil {
			return endpointPair{}, fmt.Errorf("shadow %s has missing or invalid direct-connection data: %w", shadow.GetName(), err)
		}
		if len(data.ByCluster) != 1 || len(data.ByCluster[cluster]) != 1 {
			return endpointPair{}, fmt.Errorf("shadow %s does not describe exactly one endpoint in %s", shadow.GetName(), cluster)
		}
		direct, ok := byName[shadow.GetName()]
		if !ok {
			return endpointPair{}, fmt.Errorf("direct EndpointSlice %s not found", shadow.GetName())
		}
		indirect, ok := byName[shadow.GetName()+"-indirect"]
		if !ok || indirect.Labels[IsIndirectLabel] != "true" {
			return endpointPair{}, fmt.Errorf("indirect companion for %s not found", shadow.GetName())
		}
		if len(direct.Endpoints) != 1 || len(indirect.Endpoints) != 1 {
			return endpointPair{}, fmt.Errorf("expected exactly one endpoint in each slice of %s", shadow.GetName())
		}
		d, i := direct.Endpoints[0], indirect.Endpoints[0]
		if d.TargetRef == nil || i.TargetRef == nil || d.TargetRef.UID == "" || d.TargetRef.UID != i.TargetRef.UID {
			return endpointPair{}, fmt.Errorf("endpoint identities do not match for %s", shadow.GetName())
		}
		shadowEndpoints, found, err := unstructured.NestedSlice(shadow.Object, "spec", "template", "endpoints")
		if err != nil || !found || len(shadowEndpoints) != 1 {
			return endpointPair{}, fmt.Errorf("shadow %s does not contain exactly one endpoint", shadow.GetName())
		}
		shadowEndpoint, ok := shadowEndpoints[0].(map[string]interface{})
		if !ok {
			return endpointPair{}, fmt.Errorf("invalid shadow endpoint in %s", shadow.GetName())
		}
		uid, _, _ := unstructured.NestedString(shadowEndpoint, "targetRef", "uid")
		addresses, _, err := unstructured.NestedStringSlice(shadowEndpoint, "addresses")
		if err != nil || uid != string(d.TargetRef.UID) || len(addresses) != 1 || addresses[0] != data.ByCluster[cluster][0] {
			return endpointPair{}, fmt.Errorf("shadow endpoint identity or cluster mapping does not match %s", shadow.GetName())
		}
		if (d.Conditions.Ready != nil && !*d.Conditions.Ready) || (i.Conditions.Ready != nil && !*i.Conditions.Ready) {
			return endpointPair{}, fmt.Errorf("endpoint %s is not ready", d.TargetRef.UID)
		}
		if len(d.Addresses) != 1 || len(i.Addresses) != 1 || net.ParseIP(d.Addresses[0]) == nil ||
			net.ParseIP(i.Addresses[0]) == nil || d.Addresses[0] == i.Addresses[0] {
			return endpointPair{}, fmt.Errorf("invalid or identical addresses for endpoint %s", d.TargetRef.UID)
		}
		pairs = append(pairs, endpointPair{directName: shadow.GetName(), uid: d.TargetRef.UID, directIP: d.Addresses[0], indirectIP: i.Addresses[0]})
	}
	if len(pairs) != 1 {
		return endpointPair{}, fmt.Errorf("expected exactly one remote endpoint pair for %s in %s, found %d", service, cluster, len(pairs))
	}
	for _, slice := range slices {
		if slice.Name == pairs[0].directName || slice.Name == pairs[0].directName+"-indirect" ||
			slice.Labels[ServiceNameLabel] != service {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if len(endpoint.Addresses) > 0 && (endpoint.Conditions.Ready == nil || *endpoint.Conditions.Ready) {
				return endpointPair{}, fmt.Errorf("service %s has another ready endpoint in %s", service, slice.Name)
			}
		}
	}
	return pairs[0], nil
}

func (lc *LatencyController) findPair() (endpointPair, error) {
	shadows, err := lc.dynamic.Resource(shadowResource).Namespace(lc.config.Namespace).List(lc.ctx, metav1.ListOptions{})
	if err != nil {
		return endpointPair{}, fmt.Errorf("list ShadowEndpointSlices: %w", err)
	}
	slices, err := lc.clientset.DiscoveryV1().EndpointSlices(lc.config.Namespace).List(lc.ctx, metav1.ListOptions{})
	if err != nil {
		return endpointPair{}, fmt.Errorf("list EndpointSlices: %w", err)
	}
	return selectPair(shadows.Items, slices.Items, lc.config.TargetService, lc.config.TargetCluster)
}

// measureLatency runs ping commands and calculates latency statistics
func (lc *LatencyController) measureLatency(ip string) (*LatencyMetrics, error) {
	// Build ping command
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("invalid IP %q", ip)
	}
	ctx, cancel := context.WithTimeout(lc.ctx, time.Duration(lc.config.PingCount*3+5)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", "-c", strconv.Itoa(lc.config.PingCount), "-W", "2", ip)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ping %s failed: %w: %s", ip, err, strings.TrimSpace(string(output)))
	}

	return parsePingOutput(string(output))
}

// parsePingOutput parses ping output and extracts min/avg/max/stddev
func parsePingOutput(output string) (*LatencyMetrics, error) {
	metrics := &LatencyMetrics{}

	// Look for the statistics line which typically looks like:
	// min/avg/max/stddev = 1.234/2.345/3.456/0.567 ms
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "min/avg/max") || strings.Contains(line, "min/mean/max") {
			// Parse the statistics
			parts := strings.Split(line, "=")
			if len(parts) < 2 {
				continue
			}

			statsPart := strings.TrimSpace(parts[1])
			// Remove " ms" suffix
			statsPart = strings.TrimSuffix(statsPart, " ms")

			values := strings.Split(statsPart, "/")
			if len(values) < 3 {
				continue
			}

			fields := []*float64{&metrics.Min, &metrics.Avg, &metrics.Max, &metrics.StdDev}
			for i := 0; i < len(fields) && i < len(values); i++ {
				value, err := strconv.ParseFloat(strings.TrimSpace(values[i]), 64)
				if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return nil, fmt.Errorf("invalid ping statistic %q", values[i])
				}
				*fields[i] = value
			}
			return metrics, nil
		}
	}

	return nil, fmt.Errorf("could not parse ping output")
}

// decideAndSwitch makes routing decision and switches paths if needed
func (lc *LatencyController) decideAndSwitch(pair endpointPair, directMetrics, indirectMetrics *LatencyMetrics) {
	if directMetrics.Avg <= 0 || indirectMetrics.Avg <= 0 ||
		math.IsNaN(directMetrics.Avg) || math.IsNaN(indirectMetrics.Avg) ||
		math.IsInf(directMetrics.Avg, 0) || math.IsInf(indirectMetrics.Avg, 0) {
		klog.Error("Cannot compare non-positive latency measurements")
		return
	}
	// Calculate latency ratio
	ratio := indirectMetrics.Avg / directMetrics.Avg

	klog.V(2).Infof("Latency ratio (indirect/direct): %.2f", ratio)

	// Decide which path is better
	if ratio > 1.0+lc.config.LatencyThreshold/100.0 {
		// Indirect path is significantly slower, use direct
		klog.Infof("Direct path preferred (indirect/direct ratio %.2f). Requesting switch.", ratio)
		if err := lc.switchPath(pair, false); err != nil {
			klog.Errorf("Failed to switch to direct path: %v", err)
		}
	} else if ratio < 1.0-lc.config.LatencyThreshold/100.0 {
		// Direct path is significantly slower, use indirect
		klog.Infof("Indirect path preferred (indirect/direct ratio %.2f). Requesting switch.", ratio)
		if err := lc.switchPath(pair, true); err != nil {
			klog.Errorf("Failed to switch to indirect path: %v", err)
		}
	} else {
		klog.V(2).Infof("Latency difference within threshold (%.2f%%). No switch needed.", lc.config.LatencyThreshold)
	}
}

func (lc *LatencyController) switchPath(pair endpointPair, indirect bool) error {
	client := lc.clientset.DiscoveryV1().EndpointSlices(lc.config.Namespace)
	current, err := client.Get(lc.ctx, pair.directName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get direct EndpointSlice: %w", err)
	}
	// Refuse to steer a replacement slice after measurements of an earlier endpoint.
	if len(current.Endpoints) != 1 || current.Endpoints[0].TargetRef == nil ||
		current.Endpoints[0].TargetRef.UID != pair.uid || len(current.Endpoints[0].Addresses) != 1 ||
		current.Endpoints[0].Addresses[0] != pair.directIP {
		return fmt.Errorf("endpoint changed during measurement")
	}
	var preference interface{}
	if indirect {
		preference = "true"
	}
	meta := map[string]interface{}{
		"resourceVersion": current.ResourceVersion,
		"annotations":     map[string]interface{}{latencyPreference: preference},
	}
	if indirect {
		meta["labels"] = map[string]interface{}{ServiceNameLabel: nil}
	}
	patch, err := json.Marshal(map[string]interface{}{"metadata": meta})
	if err != nil {
		return err
	}
	if (current.Annotations[latencyPreference] == "true") != indirect ||
		(indirect && current.Labels[ServiceNameLabel] != "") {
		if _, err := client.Patch(lc.ctx, pair.directName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("patch latency preference: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(lc.ctx, 30*time.Second)
	defer cancel()
	for {
		direct, err := client.Get(ctx, pair.directName, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("verify direct EndpointSlice: %w", err)
		}
		alternate, err := client.Get(ctx, pair.directName+"-indirect", metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("verify indirect EndpointSlice: %w", err)
		}
		if len(direct.Endpoints) != 1 || direct.Endpoints[0].TargetRef == nil ||
			direct.Endpoints[0].TargetRef.UID != pair.uid || len(direct.Endpoints[0].Addresses) != 1 ||
			direct.Endpoints[0].Addresses[0] != pair.directIP ||
			len(alternate.Endpoints) != 1 || alternate.Endpoints[0].TargetRef == nil ||
			alternate.Endpoints[0].TargetRef.UID != pair.uid || len(alternate.Endpoints[0].Addresses) != 1 ||
			alternate.Endpoints[0].Addresses[0] != pair.indirectIP {
			return fmt.Errorf("endpoint pair changed while waiting for switch")
		}
		if !indirect && direct.Annotations[networkFailover] == "true" {
			return fmt.Errorf("network failover is active; direct path cannot be confirmed")
		}
		directActive := direct.Labels[ServiceNameLabel] == lc.config.TargetService
		indirectActive := alternate.Labels[ServiceNameLabel] == lc.config.TargetService
		if directActive != indirectActive && indirectActive == indirect {
			klog.Infof("Confirmed %s path for %s (endpoint %s)", map[bool]string{true: "indirect", false: "direct"}[indirect], lc.config.TargetService, pair.uid)
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Liqo to apply the selected path: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func main() {
	// Configure logging
	klog.InitFlags(nil)

	// Parse environment variables for configuration
	namespace := os.Getenv("NAMESPACE")
	if namespace == "" {
		namespace = DefaultNamespace
	}

	targetService := os.Getenv("TARGET_SERVICE")
	if targetService == "" {
		targetService = "demo-server" // Default target service
	}
	targetCluster := os.Getenv("TARGET_CLUSTER")
	if targetCluster == "" {
		targetCluster = "provider2"
	}

	thresholdStr := os.Getenv("LATENCY_THRESHOLD")
	threshold := DefaultLatencyThreshold
	if thresholdStr != "" {
		val, err := strconv.ParseFloat(thresholdStr, 64)
		if err != nil || val < 0 || val >= 100 || math.IsNaN(val) || math.IsInf(val, 0) {
			klog.Fatalf("LATENCY_THRESHOLD must be a percentage in [0,100): %q", thresholdStr)
		}
		threshold = val
	}

	intervalStr := os.Getenv("MEASURE_INTERVAL")
	interval := DefaultMeasureInterval
	if intervalStr != "" {
		val, err := strconv.Atoi(intervalStr)
		if err != nil || val <= 0 {
			klog.Fatalf("MEASURE_INTERVAL must be a positive number of seconds: %q", intervalStr)
		}
		interval = val
	}

	config := &ControllerConfig{
		Namespace:        namespace,
		LatencyThreshold: threshold,
		MeasureInterval:  time.Duration(interval) * time.Second,
		PingCount:        PingCount,
		TargetService:    targetService,
		TargetCluster:    targetCluster,
	}

	// Create and run controller
	controller, err := NewLatencyController(config)
	if err != nil {
		klog.Fatalf("Failed to create controller: %v", err)
	}

	stopCh := make(chan struct{})
	controller.Run(stopCh)
}
