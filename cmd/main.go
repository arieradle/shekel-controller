// Copyright (c) 2026 Arie Radle. MIT License.

// main is a placeholder entrypoint. The controller and webhook are wired up in SHEK-14.
package main

import (
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	shekelv1alpha1 "github.com/arieradle/shekel-controller/api/v1alpha1"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(shekelv1alpha1.AddToScheme(scheme))
}

func main() {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zap.Options{})))
	log := ctrl.Log.WithName("main")
	log.Info("shekel-controller starting", "version", "v0.0.1")
	os.Exit(0)
}
