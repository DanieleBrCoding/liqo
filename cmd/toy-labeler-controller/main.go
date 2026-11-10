package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// Configuration constants
	DefaultNamespace        = "hub-spoke-demo"
	DirectServiceName       = "pinger-direct-svc"
	IndirectServiceName     = "pinger-indirect-svc"
	DefaultLatencyThreshold = 2.0 // milliseconds - threshold for switching paths
	DefaultMeasureInterval  = 30  // seconds - how often to measure
	PingCount               = 5   // number of ping requests per measurement

	// Labels for identifying EndpointSlices
	ServiceNameLabel   = "kubernetes.io/service-name"
	IsIndirectLabel    = "liqo.io/indirect-endpointslice"
	TargetServiceLabel = "liqo.io/service-target"
)

type LatencyMetrics struct {
	Min    float64
	Avg    float64
	Max    float64
	StdDev float64
}

type ControllerConfig struct {
	Namespace        string
	DirectService    string
	IndirectService  string
	LatencyThreshold float64
	MeasureInterval  time.Duration
	PingCount        int
	TargetService    string // The service to switch (e.g., "demo-server")
}

type LatencyController struct {
	config    *ControllerConfig
	clientset kubernetes.Interface
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

	return &LatencyController{
		config:    config,
		clientset: clientset,
		ctx:       context.Background(),
	}, nil
}

// Run starts the main controller loop
func (lc *LatencyController) Run(stopCh <-chan struct{}) {
	klog.Infof("Starting Latency Controller")
	klog.Infof("  Namespace: %s", lc.config.Namespace)
	klog.Infof("  Target Service: %s", lc.config.TargetService)
	klog.Infof("  Latency Threshold: %.2f ms", lc.config.LatencyThreshold)
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
	klog.V(2).Infof("Starting latency measurement cycle")

	// Get EndpointSlice IPs for both services
	directIPs, err := lc.getEndpointSliceIPs(lc.config.DirectService)
	if err != nil {
		klog.Errorf("Failed to get direct service IPs: %v", err)
		return
	}

	indirectIPs, err := lc.getEndpointSliceIPs(lc.config.IndirectService)
	if err != nil {
		klog.Errorf("Failed to get indirect service IPs: %v", err)
		return
	}

	if len(directIPs) == 0 || len(indirectIPs) == 0 {
		klog.Warningf("Missing endpoints: direct=%d, indirect=%d", len(directIPs), len(indirectIPs))
		return
	}

	// Pick the first available IP from each service
	directIP := directIPs[0]
	indirectIP := indirectIPs[0]

	klog.V(3).Infof("Measuring latency - Direct: %s, Indirect: %s", directIP, indirectIP)

	// Measure latency for both paths
	directMetrics, err := lc.measureLatency(directIP)
	if err != nil {
		klog.Errorf("Failed to measure direct path latency: %v", err)
		return
	}

	indirectMetrics, err := lc.measureLatency(indirectIP)
	if err != nil {
		klog.Errorf("Failed to measure indirect path latency: %v", err)
		return
	}

	// Log results
	klog.Infof("Latency Results (avg):")
	klog.Infof("  Direct:   %.2f ms (min=%.2f, max=%.2f)", directMetrics.Avg, directMetrics.Min, directMetrics.Max)
	klog.Infof("  Indirect: %.2f ms (min=%.2f, max=%.2f)", indirectMetrics.Avg, indirectMetrics.Min, indirectMetrics.Max)

	// Decide which path to use
	lc.decideAndSwitch(directMetrics, indirectMetrics)
}

// getEndpointSliceIPs retrieves the IPs from an EndpointSlice for a given service
func (lc *LatencyController) getEndpointSliceIPs(serviceName string) ([]string, error) {
	discoveryClient := lc.clientset.DiscoveryV1()

	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", ServiceNameLabel, serviceName),
	}

	slices, err := discoveryClient.EndpointSlices(lc.config.Namespace).List(lc.ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list EndpointSlices: %w", err)
	}

	if len(slices.Items) == 0 {
		return nil, fmt.Errorf("no EndpointSlices found for service %s", serviceName)
	}

	var ips []string
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Addresses != nil {
				ips = append(ips, endpoint.Addresses...)
			}
		}
	}

	return ips, nil
}

// measureLatency runs ping commands and calculates latency statistics
func (lc *LatencyController) measureLatency(ip string) (*LatencyMetrics, error) {
	// Build ping command
	pingCmd := fmt.Sprintf("ping -c %d %s", lc.config.PingCount, ip)

	cmd := exec.CommandContext(lc.ctx, "sh", "-c", pingCmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Ping might fail but we still want to parse partial output
		klog.V(3).Infof("Ping command error (may be partial): %v", err)
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

			if minVal, err := strconv.ParseFloat(strings.TrimSpace(values[0]), 64); err == nil {
				metrics.Min = minVal
			}

			if avgVal, err := strconv.ParseFloat(strings.TrimSpace(values[1]), 64); err == nil {
				metrics.Avg = avgVal
			}

			if maxVal, err := strconv.ParseFloat(strings.TrimSpace(values[2]), 64); err == nil {
				metrics.Max = maxVal
			}

			if len(values) > 3 {
				if stdVal, err := strconv.ParseFloat(strings.TrimSpace(values[3]), 64); err == nil {
					metrics.StdDev = stdVal
				}
			}

			return metrics, nil
		}
	}

	return nil, fmt.Errorf("could not parse ping output")
}

// decideAndSwitch makes routing decision and switches paths if needed
func (lc *LatencyController) decideAndSwitch(directMetrics, indirectMetrics *LatencyMetrics) {
	// Calculate latency ratio
	ratio := indirectMetrics.Avg / directMetrics.Avg

	klog.V(2).Infof("Latency ratio (indirect/direct): %.2f", ratio)

	// Decide which path is better
	if ratio > 1.0+lc.config.LatencyThreshold/100.0 {
		// Indirect path is significantly slower, use direct
		klog.Infof("Direct path is better (%.2f%% faster). Ensuring direct path is active.", (ratio-1)*100)
		if err := lc.switchToDirectPath(); err != nil {
			klog.Errorf("Failed to switch to direct path: %v", err)
		}
	} else if ratio < 1.0-lc.config.LatencyThreshold/100.0 {
		// Direct path is significantly slower, use indirect
		klog.Infof("Indirect path is better (%.2f%% faster). Switching to indirect path.", (1-ratio)*100)
		if err := lc.switchToIndirectPath(); err != nil {
			klog.Errorf("Failed to switch to indirect path: %v", err)
		}
	} else {
		klog.V(2).Infof("Latency difference within threshold (%.2f%%). No switch needed.", lc.config.LatencyThreshold)
	}
}

// switchToDirectPath modifies EndpointSlices to use direct routing
func (lc *LatencyController) switchToDirectPath() error {
	klog.V(2).Infof("Switching to direct path")

	// Find and update the EndpointSlices
	return lc.updateEndpointSliceLabels(true)
}

// switchToIndirectPath modifies EndpointSlices to use indirect routing
func (lc *LatencyController) switchToIndirectPath() error {
	klog.V(2).Infof("Switching to indirect path")

	// Find and update the EndpointSlices
	return lc.updateEndpointSliceLabels(false)
}

// updateEndpointSliceLabels switches the active path by moving the service label
func (lc *LatencyController) updateEndpointSliceLabels(useDirect bool) error {
	discoveryClient := lc.clientset.DiscoveryV1()

	// List all EndpointSlices for our target service
	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", TargetServiceLabel, lc.config.TargetService),
	}

	slices, err := discoveryClient.EndpointSlices(lc.config.Namespace).List(lc.ctx, listOptions)
	if err != nil {
		return fmt.Errorf("failed to list EndpointSlices: %w", err)
	}

	for i := range slices.Items {
		slice := &slices.Items[i]

		if useDirect {
			// Activate direct slice, deactivate indirect
			if slice.Labels[IsIndirectLabel] == "true" {
				// This is the indirect slice, remove service label
				delete(slice.Labels, ServiceNameLabel)
				klog.V(2).Infof("Removed service label from indirect EndpointSlice %s", slice.Name)
			} else {
				// This is the direct slice, add service label
				if slice.Labels == nil {
					slice.Labels = make(map[string]string)
				}
				slice.Labels[ServiceNameLabel] = lc.config.TargetService
				klog.V(2).Infof("Added service label to direct EndpointSlice %s", slice.Name)
			}
		} else {
			// Activate indirect slice, deactivate direct
			if slice.Labels[IsIndirectLabel] == "true" {
				// This is the indirect slice, add service label
				if slice.Labels == nil {
					slice.Labels = make(map[string]string)
				}
				slice.Labels[ServiceNameLabel] = lc.config.TargetService
				klog.V(2).Infof("Added service label to indirect EndpointSlice %s", slice.Name)
			} else {
				// This is the direct slice, remove service label
				delete(slice.Labels, ServiceNameLabel)
				klog.V(2).Infof("Removed service label from direct EndpointSlice %s", slice.Name)
			}
		}

		// Update the EndpointSlice
		_, err := discoveryClient.EndpointSlices(lc.config.Namespace).Update(lc.ctx, slice, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("Failed to update EndpointSlice %s: %v", slice.Name, err)
			continue
		}

		klog.Infof("Successfully updated EndpointSlice %s", slice.Name)
	}

	return nil
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

	thresholdStr := os.Getenv("LATENCY_THRESHOLD")
	threshold := DefaultLatencyThreshold
	if thresholdStr != "" {
		if val, err := strconv.ParseFloat(thresholdStr, 64); err == nil {
			threshold = val
		}
	}

	intervalStr := os.Getenv("MEASURE_INTERVAL")
	interval := DefaultMeasureInterval
	if intervalStr != "" {
		if val, err := strconv.Atoi(intervalStr); err == nil {
			interval = val
		}
	}

	config := &ControllerConfig{
		Namespace:        namespace,
		DirectService:    DirectServiceName,
		IndirectService:  IndirectServiceName,
		LatencyThreshold: threshold,
		MeasureInterval:  time.Duration(interval) * time.Second,
		PingCount:        PingCount,
		TargetService:    targetService,
	}

	// Create and run controller
	controller, err := NewLatencyController(config)
	if err != nil {
		klog.Fatalf("Failed to create controller: %v", err)
	}

	stopCh := make(chan struct{})
	controller.Run(stopCh)
}
