package generic

import (
	"context"
	"fmt"
	"sync"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	gocache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// SyncRuleReconciler reconciles a SyncRule object
type SyncRuleReconciler struct {
	AppCtx context.Context
	client.Client
	Scheme *runtime.Scheme

	// Use the manager's components
	Cache      cache.Cache
	Config     *rest.Config
	RESTMapper meta.RESTMapper

	// Shared factory for all dynamic informers, created once.
	dynamicClient   dynamic.Interface
	informerFactory dynamicinformer.DynamicSharedInformerFactory

	// dynamic controllers lifecycle management
	activeControllers   map[types.NamespacedName]context.CancelFunc
	activeControllersMu sync.RWMutex
}

func SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	dynamicClient, err := dynamic.NewForConfig(mgr.GetConfig())
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	informerFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, 0)

	r := &SyncRuleReconciler{
		AppCtx:            ctx,
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Cache:             mgr.GetCache(),
		Config:            mgr.GetConfig(),
		RESTMapper:        mgr.GetRESTMapper(),
		dynamicClient:     dynamicClient,
		informerFactory:   informerFactory,
		activeControllers: make(map[types.NamespacedName]context.CancelFunc),
	}

	if err := mgr.Add(r); err != nil {
		return fmt.Errorf("failed to add runnable to manager: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SyncRule{}).
		Complete(r)
}

// Start is called by the Manager. It starts the dynamic informer factory.
func (r *SyncRuleReconciler) Start(ctx context.Context) error {
	logger := log.Log.WithName("syncrule-runnable")
	logger.Info("SyncRuleReconciler runnable has been started by the manager.")

	r.informerFactory.Start(ctx.Done())

	return nil
}

func (r *SyncRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("syncrule", req.NamespacedName)

	var syncRule v1alpha1.SyncRule
	if err := r.Get(ctx, req.NamespacedName, &syncRule); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("SyncRule not found. Stopping associated watcher.")
			r.stopDynamicController(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get SyncRule: %w", err)
	}

	if !syncRule.DeletionTimestamp.IsZero() {
		logger.Info("SyncRule is being deleted. Stopping watcher.")
		r.stopDynamicController(req.NamespacedName)
		return ctrl.Result{}, nil
	}

	logger.Info("Reconciling SyncRule, stopping any existing watcher to apply latest config.")
	r.stopDynamicController(req.NamespacedName)

	sourceGVK := schema.GroupVersionKind{
		Group:   syncRule.Spec.SourceGVK.Group,
		Version: syncRule.Spec.SourceGVK.Version,
		Kind:    syncRule.Spec.SourceGVK.Kind,
	}

	syncerConfig := DynamicSyncerConfig{
		RuleNamespacedName: req.NamespacedName, // The key of the SyncRule CR itself
		SourceGVK:          sourceGVK,
		SourceNamespace:    syncRule.Spec.SourceNamespace,
		TargetClusterRef:   syncRule.Spec.TargetCluster,
		TargetNamespace:    syncRule.Spec.TargetNamespace,
	}

	// Pass the main application context as the parent
	if err := r.startDynamicController(r.AppCtx, req.NamespacedName, syncerConfig); err != nil {
		logger.Error(err, "Failed to start dynamic watcher for rule")
		return ctrl.Result{}, err
	}

	logger.Info("Dynamic watcher was successfully started/restarted for rule.")
	return ctrl.Result{}, nil
}

func (r *SyncRuleReconciler) startDynamicController(
	parentCtx context.Context,
	ruleKey types.NamespacedName,
	config DynamicSyncerConfig,
) error {
	logger := log.FromContext(parentCtx).WithValues("rule", ruleKey, "gvk", config.SourceGVK)

	mapping, err := r.RESTMapper.RESTMapping(config.SourceGVK.GroupKind(), config.SourceGVK.Version)
	if err != nil {
		return fmt.Errorf("failed to map GVK %v to GVR: %w", config.SourceGVK, err)
	}

	informer := r.informerFactory.ForResource(mapping.Resource).Informer()

	rateLimiter := workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]()
	queue := workqueue.NewTypedRateLimitingQueueWithConfig(rateLimiter, workqueue.TypedRateLimitingQueueConfig[reconcile.Request]{
		Name: fmt.Sprintf("syncer-%s", ruleKey.String()),
	})
	config.Workqueue = queue

	// The reconciler logic
	dynamicSyncer := &DynamicResourceSyncer{
		Client: r.Client,
		Scheme: r.Scheme,
		Config: config,
	}

	// Create a cancellable context for the informer and worker goroutine
	controllerCtx, cancelFunc := context.WithCancel(parentCtx)

	// // Get an informer for the specific GVK from the manager's cache
	// informer, err := r.Manager.GetCache().GetInformerForKind(controllerCtx, config.SourceGVK)
	// if err != nil {
	// 	cancelFunc() // clean up context if we fail
	// 	return fmt.Errorf("failed to get informer for GVK %v: %w", config.SourceGVK, err)
	// }

	logger.Info("Setting up event handler for dynamic watcher.")
	// Add an event handler that simply adds the object's key to our new workqueue
	handlerRegistration, err := informer.AddEventHandler(gocache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj any) { queue.Add(reconcile.Request{NamespacedName: objectToKey(obj)}) },
		UpdateFunc: func(oldObj, newObj any) { queue.Add(reconcile.Request{NamespacedName: objectToKey(newObj)}) },
		DeleteFunc: func(obj any) { queue.Add(reconcile.Request{NamespacedName: objectToKey(obj)}) },
	})
	if err != nil {
		cancelFunc() // clean up context if we fail
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	// Store the cancel function so we can stop this later
	r.activeControllersMu.Lock()
	r.activeControllers[ruleKey] = cancelFunc
	r.activeControllersMu.Unlock()

	// Start a worker goroutine that processes items from the workqueue
	logger.Info("Starting worker goroutine for dynamic watcher.")

	go func() {
		defer func() {
			cancelFunc()

			if err := informer.RemoveEventHandler(handlerRegistration); err != nil {
				logger.Error(err, "Failed to remove event handler from informer")
			}

			queue.ShutDown()
			logger.Info("Worker goroutine has stopped and cleaned up.")
		}()

		// Wait until the informer's cache is synced before starting to process items.
		// This prevents the worker from processing items before the cache is ready.
		if !gocache.WaitForCacheSync(parentCtx.Done(), informer.HasSynced) {
			logger.Error(nil, "Timed out waiting for informer cache to sync")
			return
		}

		logger.Info("Informer cache synced. Worker is starting.")

		// This is the standard worker loop pattern
		for dynamicSyncer.processNextWorkItem(controllerCtx, logger) {
		}

		logger.Info("Worker goroutine has stopped.")
	}()

	return nil
}

// objectToKey is a helper to get the key from an object in the informer
func objectToKey(obj any) types.NamespacedName {
	metaObj, ok := obj.(metav1.Object)
	if !ok {
		tombstone, ok := obj.(gocache.DeletedFinalStateUnknown)
		if !ok {
			log.Log.Error(nil, "error decoding object, invalid type")
			return types.NamespacedName{}
		}
		metaObj, ok = tombstone.Obj.(metav1.Object)
		if !ok {
			log.Log.Error(nil, "error decoding object tombstone, invalid type")
			return types.NamespacedName{}
		}
	}
	return types.NamespacedName{Name: metaObj.GetName(), Namespace: metaObj.GetNamespace()}
}

func (r *SyncRuleReconciler) stopDynamicController(ruleKey types.NamespacedName) {
	r.activeControllersMu.Lock()
	defer r.activeControllersMu.Unlock()

	if cancel, exists := r.activeControllers[ruleKey]; exists {
		log.Log.Info("Stopping dynamic controller for rule", "syncrule", ruleKey)
		cancel()
		delete(r.activeControllers, ruleKey)
	}
}
