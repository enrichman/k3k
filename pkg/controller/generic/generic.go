package generic

import (
	"context"
	"reflect"
	"time"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/rancher/k3k/pkg/controller/cluster/server"
	"github.com/rancher/k3k/pkg/controller/kubeconfig"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

type GenericReconciler struct {
	Client client.Client
	Scheme *runtime.Scheme
	GKV    schema.GroupVersionKind
}

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

	cluster := &v1alpha1.Cluster{ObjectMeta: v1.ObjectMeta{Name: "mycluster3", Namespace: "k3k-mycluster3"}}
	endpoint := server.ServiceName(cluster.Name) + "." + cluster.Namespace

	kubeCfg, err := kubeconfig.New().Extract(ctx, c.Client, cluster, endpoint)
	if err != nil {
		return reconcile.Result{}, err
	}

	kubeCfg.Clusters[kubeCfg.CurrentContext].Server = "https://" + endpoint

	clientConfig := clientcmd.NewDefaultClientConfig(*kubeCfg, &clientcmd.ConfigOverrides{})

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return reconcile.Result{}, err
	}

	vclusterClient, err := client.New(restConfig, client.Options{Scheme: c.Scheme})
	if err != nil {
		return reconcile.Result{}, err
	}

	hostResource := &unstructured.Unstructured{}
	hostResource.SetGroupVersionKind(c.GKV)

	if err := c.Client.Get(ctx, req.NamespacedName, hostResource); err != nil {
		return reconcile.Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	targetResource := &unstructured.Unstructured{}
	targetResource.SetGroupVersionKind(c.GKV)
	targetResource.SetName(hostResource.GetName())
	targetResource.SetNamespace(hostResource.GetNamespace())

	operationResult, err := controllerutil.CreateOrUpdate(ctx, vclusterClient, targetResource, func() error {
		targetResource.SetGroupVersionKind(c.GKV)
		targetResource.SetName(hostResource.GetName())
		targetResource.SetNamespace(hostResource.GetNamespace())

		targetResource.SetLabels(hostResource.GetLabels())
		targetResource.SetAnnotations(hostResource.GetAnnotations())

		// set spec
		spec, _, err := unstructured.NestedMap(hostResource.Object, "spec")
		if err != nil {
			return err
		}

		if err := unstructured.SetNestedMap(targetResource.Object, spec, "spec"); err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		return reconcile.Result{}, err
	}

	log.Info("generic resource updated", "result", operationResult)

	currentStatus, _, _ := unstructured.NestedMap(hostResource.Object, "status")
	targetStatus, _, _ := unstructured.NestedMap(targetResource.Object, "status")

	if !reflect.DeepEqual(currentStatus, targetStatus) {
		if err := unstructured.SetNestedMap(targetResource.Object, currentStatus, "status"); err != nil {
			return reconcile.Result{}, err
		}

		if err := vclusterClient.Status().Update(ctx, targetResource); err != nil {
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}
