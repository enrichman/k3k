package generic

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// SyncRuleReconciler reconciles a SyncRule object
type SyncRuleReconciler struct {
	client.Client
	*runtime.Scheme
	ctrl.Manager

	managerStarted bool

	// dynamic controllers lifecycle management
	activeControllers   map[string]*DynamicController
	activeControllersMu sync.RWMutex
}

func SetupWithManager(mgr ctrl.Manager) error {
	r := &SyncRuleReconciler{
		Client:            mgr.GetClient(),
		Scheme:            mgr.GetScheme(),
		Manager:           mgr,
		activeControllers: make(map[string]*DynamicController),
	}

	// Listen for manager start to set flag
	go func() {
		<-mgr.Elected()

		r.activeControllersMu.Lock()
		r.managerStarted = true
		r.activeControllersMu.Unlock()
	}()

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SyncRule{}).
		Complete(r)
}

func (r *SyncRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("syncrule", req.NamespacedName)

	var syncRule v1alpha1.SyncRule
	if err := r.Get(ctx, req.NamespacedName, &syncRule); err != nil {
		if kerrors.IsNotFound(err) {
			logger.Info("SyncRule not found")

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("failed to get SyncRule: %w", err)
	}

	// Group, Kind and Version of the resource to watch
	sourceGVK := schema.GroupVersionKind{
		Group:   syncRule.Spec.SourceGVK.Group,
		Version: syncRule.Spec.SourceGVK.Version,
		Kind:    syncRule.Spec.SourceGVK.Kind,
	}

	syncerConfig := DynamicSyncerConfig{
		RuleNamespacedName: req.NamespacedName,
		SourceGVK:          sourceGVK,
		SourceNamespace:    syncRule.Spec.SourceNamespace,
		TargetClusterRef:   syncRule.Spec.TargetCluster,
		TargetNamespace:    syncRule.Spec.TargetNamespace,
	}

	if err := r.startDynamicController(ctx, req.NamespacedName, syncerConfig); err != nil {
		logger.Error(err, "Failed to start dynamic controller for rule")

		return ctrl.Result{}, err
	}

	logger.Info("Dynamic controller initiated for rule.")

	// TODO: Update status of SyncRule ("Active")

	return ctrl.Result{}, nil
}

func (r *SyncRuleReconciler) startDynamicController(
	ctx context.Context,
	ruleKey types.NamespacedName,
	config DynamicSyncerConfig,
) error {
	r.activeControllersMu.Lock()
	defer r.activeControllersMu.Unlock()

	// Wait for main manager to be started
	if !r.managerStarted {
		return fmt.Errorf("main manager not yet started, requeuing")
	}

	// Create a unique key for this controller
	controllerName := fmt.Sprintf("%s-%s-%s-%s-%d",
		config.SourceGVK.Group,
		config.SourceGVK.Version,
		config.SourceGVK.Kind,
		ruleKey.String(),
		time.Now().Unix(),
	)
	controllerName = strings.ToLower(strings.ReplaceAll(controllerName, "/", "-"))

	logger := log.FromContext(ctx).WithValues("dynamicController", controllerName, "rule", ruleKey)

	// Check if controller already exists and is running
	if existingController, exists := r.activeControllers[controllerName]; exists {
		existingController.mutex.RLock()
		isStarted := existingController.Started
		existingController.mutex.RUnlock()

		if isStarted {
			logger.Info("Controller already running", "controllerName", controllerName)
			return nil
		}

		// Clean up the non-started controller
		r.cleanupController(controllerName, existingController)
	}

	// Create new controller with proper context management
	dynCtx, dynCancel := context.WithCancel(context.Background())

	// Create a new manager for this dynamic controller
	restConfig := r.Manager.GetConfig()

	dynamicMgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 r.Scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"}, // Disable metrics for dynamic controllers
		HealthProbeBindAddress: "0",                                     // Disable health probe
		LeaderElection:         false,                                   // Disable leader election for dynamic controllers
		Cache: cache.Options{
			// Only watch the specific namespace if specified
			DefaultNamespaces: map[string]cache.Config{
				config.SourceNamespace: {},
			},
		},
	})
	if err != nil {
		dynCancel()
		return fmt.Errorf("failed to create dynamic manager: %w", err)
	}

	dynamicController := &DynamicController{
		Name:    controllerName,
		Context: dynCtx,
		Cancel:  dynCancel,
		Manager: dynamicMgr,
		Started: false,
	}

	r.activeControllers[controllerName] = dynamicController

	// Create the reconciler
	dynamicSyncer := &DynamicResourceSyncer{
		Client: dynamicMgr.GetClient(),
		Scheme: r.Scheme,
		Config: config,
	}

	logger.Info("Creating dynamic controller", "controllerName", controllerName)

	// Create the unstructured object with the correct GVK for watching
	watchObject := &unstructured.Unstructured{}
	watchObject.SetGroupVersionKind(config.SourceGVK)

	// Set up the controller with the new manager
	err = ctrl.NewControllerManagedBy(dynamicMgr).
		// very important!
		// By default, controllers are named using the lowercase version of their kind.
		// we need to have a unique name
		Named(controllerName).
		For(watchObject).
		Complete(dynamicSyncer)

	if err != nil {
		r.cleanupController(controllerName, dynamicController)
		return fmt.Errorf("failed to create controller: %w", err)
	}

	go func() {
		defer func() {
			r.activeControllersMu.Lock()
			r.cleanupController(controllerName, dynamicController)
			r.activeControllersMu.Unlock()
		}()

		// Mark as started
		dynamicController.mutex.Lock()
		dynamicController.Started = true
		dynamicController.mutex.Unlock()

		if err := dynamicMgr.Start(dynCtx); err != nil {
			if !errors.Is(err, context.Canceled) {
				logger.Error(err, "Dynamic controller manager returned an error", "controllerName", controllerName)
			}
		}

		logger.Info("Stopped dynamic controller manager", "controllerName", controllerName)
	}()

	return nil
}

func (r *SyncRuleReconciler) cleanupController(name string, controller *DynamicController) {
	log.Log.Info("Cleaning up controller", "controllerName", name, "cancelFunc", controller.Cancel)

	if controller.Cancel != nil {
		controller.Cancel()
	}

	delete(r.activeControllers, name)

	// Give some time for graceful shutdown
	time.Sleep(2 * time.Second)
}
