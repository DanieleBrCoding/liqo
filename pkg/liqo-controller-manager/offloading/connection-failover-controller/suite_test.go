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

// Package connectionfailoverctrl — test bootstrap.
//
// # How Go tests are structured in this codebase
//
// Liqo tests use the Ginkgo v2 BDD framework (https://onsi.github.io/ginkgo/) together with
// the Gomega assertion library (https://onsi.github.io/gomega/).
//
// Every package that uses Ginkgo needs exactly one "suite bootstrap" file like this one.
// Its job is to:
//
//  1. Provide the standard TestXxx function that `go test` discovers.
//  2. Call RegisterFailHandler so that Gomega Expect() calls abort the test on failure.
//  3. Call RunSpecs to hand control to the Ginkgo runner.
//  4. Run a BeforeSuite block that executes once before any test spec — typically used to
//     register custom API types into the Kubernetes runtime scheme used by the fake client.
package connectionfailoverctrl

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/kubernetes/scheme"

	networkingv1beta1 "github.com/liqotech/liqo/apis/networking/v1beta1"
	offloadingv1beta1 "github.com/liqotech/liqo/apis/offloading/v1beta1"
	"github.com/liqotech/liqo/pkg/utils/testutil"
)

// TestConnectionFailoverController is the entry-point that `go test` looks for.
// It wires Go's standard testing infrastructure to the Ginkgo runner.
func TestConnectionFailoverController(t *testing.T) {
	RegisterFailHandler(Fail) // Gomega panics abort Ginkgo specs, not the whole binary
	RunSpecs(t, "Connection Failover Controller Suite")
}

// BeforeSuite runs once before any spec in this suite.
// We register the Kubernetes types we need into the shared scheme so that the fake
// client (used in the test specs) can marshal/unmarshal them correctly.
var _ = BeforeSuite(func() {
	// Redirect klog output to Ginkgo's writer so log lines appear in test reports.
	testutil.LogsToGinkgoWriter()

	// k8s.io/client-go/kubernetes/scheme already contains the core Kubernetes types
	// (Pod, Service, …) but NOT the discovery/v1 or liqo CRD types — add them here.
	Expect(discoveryv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(networkingv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(offloadingv1beta1.AddToScheme(scheme.Scheme)).To(Succeed())
})
