package generic

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type GenericReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	GKV    schema.GroupVersionKind
}

// resToWatch is a placeholder for the resource to watch.
// This should be configured based on the specific generic controller instance.
var resToWatch = schema.GroupVersionKind{
	Group:   "cert-manager.io",
	Version: "v1",
	Kind:    "ClusterIssuer",
}

// Add the controller to manage the Virtual Cluster policies
func Add(mgr manager.Manager) error {
	gvk := schema.GroupVersionKind{
		Group:   resToWatch.Group,
		Version: resToWatch.Version,
		Kind:    resToWatch.Kind,
	}

	reconciler := GenericReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		GKV:    gvk,
	}

	genericResource := &unstructured.Unstructured{}
	genericResource.SetGroupVersionKind(gvk)

	return ctrl.NewControllerManagedBy(mgr).
		For(genericResource).
		Complete(&reconciler)
}

func (c *GenericReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("reconciling GenericResource")

	fmt.Println("RECONCILINGGGG!!!!")

	return reconcile.Result{}, nil
}
