package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	toylabeler "github.com/liqotech/liqo/pkg/liqo-controller-manager/toy-labeler-controller"
)

func main() {
	ctrl.SetLogger(klogr.New())

	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	//Creazione Manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:  scheme,
		Metrics: server.Options{BindAddress: "0"},
	})
	if err != nil {
		klog.Errorf("Unable to start manager: %v", err)
		os.Exit(1)
	}

	//Registrazione del controller
	if err := (&toylabeler.ToyLabelerReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		klog.Errorf("Unable to set up controller: %v", err)
		os.Exit(1)
	}

	//Avvio del manager
	klog.Info("Starting toy-labeler controller")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		klog.Errorf("Error running controller: %v", err)
		os.Exit(1)
	}
}
