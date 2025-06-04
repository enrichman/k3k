package generic

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// SyncRuleReconciler reconciles a SyncRule object
type SyncRuleReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Manager           manager.Manager
	activeInformers   map[types.NamespacedName]context.CancelFunc
	activeInformersMu sync.Mutex
}

// DynamicSyncerConfig holds configuration for a specific sync operation, derived from SyncRuleSpec.
type DynamicSyncerConfig struct {
	RuleNamespacedName types.NamespacedName // For logging/identification
	SourceGVK          schema.GroupVersionKind
	SourceNamespace    string
	TargetClusterRef   v1alpha1.TargetClusterRef
	TargetNamespace    string
}

// DynamicResourceSyncer performs the actual sync logic for a given rule.
type DynamicResourceSyncer struct {
	client.Client // Client for the source cluster (where SyncRule & source objects live)
	Scheme        *runtime.Scheme
	Config        DynamicSyncerConfig
}

func SetupWithManager(mgr ctrl.Manager) error {
	r := &SyncRuleReconciler{
		Client:          mgr.GetClient(),
		Scheme:          mgr.GetScheme(),
		Manager:         mgr,
		activeInformers: make(map[types.NamespacedName]context.CancelFunc),
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SyncRule{}).
		Complete(r)
}

func (r *SyncRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("k3ksyncrule", req.NamespacedName)

	var syncRule v1alpha1.SyncRule
	if err := r.Get(ctx, req.NamespacedName, &syncRule); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("SyncRule not found, stopping associated informer.")
			r.stopInformer(req.NamespacedName)

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("failed to get SyncRule: %w", err)
	}

	if !syncRule.DeletionTimestamp.IsZero() {
		logger.Info("SyncRule is being deleted, stopping informer.")
		r.stopInformer(req.NamespacedName)

		return ctrl.Result{}, nil
	}

	// For simplicity, always stop and restart informer on any change.
	// A more advanced version could check if relevant spec fields changed.
	logger.Info("Reconciling SyncRule, will restart informer.")
	r.stopInformer(req.NamespacedName)

	sourceGVK := schema.GroupVersionKind{
		Group:   syncRule.Spec.SourceGVK.Group,
		Version: syncRule.Spec.SourceGVK.Version,
		Kind:    syncRule.Spec.SourceGVK.Kind,
	}

	syncerConfig := DynamicSyncerConfig{
		RuleNamespacedName: req.NamespacedName,
		SourceGVK:          sourceGVK,
		SourceNamespace:    syncRule.Spec.SourceNamespace, // Can be empty
		TargetClusterRef:   syncRule.Spec.TargetCluster,
		TargetNamespace:    syncRule.Spec.TargetNamespace, // Can be empty
	}

	informerCtx, cancelFunc := context.WithCancel(context.Background())
	if err := r.startDynamicWatcher(informerCtx, req.NamespacedName, syncerConfig); err != nil {
		logger.Error(err, "Failed to start dynamic watcher for rule")
		cancelFunc() // Ensure context is cancelled if start failed
		// Update status of SyncRule to indicate error
		return ctrl.Result{}, err
	}

	r.activeInformersMu.Lock()
	r.activeInformers[req.NamespacedName] = cancelFunc
	r.activeInformersMu.Unlock()

	logger.Info("Dynamic watcher started/updated for rule.")
	// Update status of SyncRule to indicate "Active" or "Synced"
	return ctrl.Result{}, nil
}

func (r *SyncRuleReconciler) startDynamicWatcher(
	ctx context.Context, // This context is for the lifetime of this specific watcher
	ruleKey types.NamespacedName,
	config DynamicSyncerConfig,
) error {
	logger := log.FromContext(ctx).WithValues("rule", ruleKey, "sourceGVK", config.SourceGVK)
	logger.Info("Starting dynamic watcher")

	dynamicSyncer := &DynamicResourceSyncer{
		Client: r.Client,
		Scheme: r.Scheme,
		Config: config,
	}

	informer, err := r.Manager.GetCache().GetInformerForKind(ctx, config.SourceGVK)
	if err != nil {
		return fmt.Errorf("failed to get informer for GVK %v: %w", config.SourceGVK, err)
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			metaObj, ok := obj.(metav1.Object)
			if !ok {
				logger.Error(fmt.Errorf("object is not metav1.Object"), "Informer AddFunc")
				return
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: metaObj.GetName(), Namespace: metaObj.GetNamespace()}}
			logger.V(1).Info("Informer Add", "request", req)
			// In production, use a workqueue
			go dynamicSyncer.Reconcile(context.Background(), req) // Use a fresh context for each reconcile
		},
		UpdateFunc: func(oldObj, newObj any) {
			metaObj, ok := newObj.(metav1.Object)
			if !ok {
				logger.Error(fmt.Errorf("new object is not metav1.Object"), "Informer UpdateFunc")
				return
			}
			// Potentially compare resourceVersion to avoid redundant reconciles if only status changed etc.
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: metaObj.GetName(), Namespace: metaObj.GetNamespace()}}
			logger.V(1).Info("Informer Update", "request", req)
			go dynamicSyncer.Reconcile(context.Background(), req)
		},
		DeleteFunc: func(obj any) {
			metaObj, ok := obj.(metav1.Object)
			if !ok {
				// Handle cases where obj might be a DeletionFinalStateUnknown
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					logger.Error(fmt.Errorf("error decoding object, invalid type"), "Informer DeleteFunc")
					return
				}
				metaObj, ok = tombstone.Obj.(metav1.Object)
				if !ok {
					logger.Error(fmt.Errorf("error decoding object tombstone, invalid type"), "Informer DeleteFunc")
					return
				}
			}
			req := ctrl.Request{NamespacedName: types.NamespacedName{Name: metaObj.GetName(), Namespace: metaObj.GetNamespace()}}
			logger.V(1).Info("Informer Delete", "request", req)
			go dynamicSyncer.Reconcile(context.Background(), req)
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler to informer for GVK %v: %w", config.SourceGVK, err)
	}

	// The informer is run by the manager's cache. We just need to ensure our context is managed.
	// The context passed to GetInformerForKind handles its lifecycle.
	go func() {
		<-ctx.Done() // Wait for the context to be cancelled
		logger.Info("Dynamic watcher context cancelled, informer should stop.", "rule", ruleKey)
	}()

	return nil
}

func (r *SyncRuleReconciler) stopInformer(ruleKey types.NamespacedName) {
	r.activeInformersMu.Lock()
	defer r.activeInformersMu.Unlock()

	if cancel, exists := r.activeInformers[ruleKey]; exists {
		log.Log.Info("Stopping dynamic watcher for rule", "k3ksyncrule", ruleKey)
		cancel() // This cancels the context passed to GetInformerForKind
		delete(r.activeInformers, ruleKey)
	}
}

// --- DynamicResourceSyncer Reconcile Method ---
// This is the core logic, adapted from your original GenericReconciler.
func (s *DynamicResourceSyncer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"dynamicSyncerRule", s.Config.RuleNamespacedName,
		"sourceObject", req.NamespacedName,
		"sourceGVK", s.Config.SourceGVK,
	)
	logger.Info("DynamicResourceSyncer processing request")

	// --- 1. Build Target K8s Client ---
	// This part is CRITICAL and relies on your specific v1alpha1.Cluster CRD and kubeconfig logic.
	// The following is a conceptual adaptation of your provided code.
	clusterCR := &v1alpha1.Cluster{} // Use your actual Cluster CRD type
	clusterCRKey := types.NamespacedName{
		Name:      s.Config.TargetClusterRef.Name,
		Namespace: s.Config.TargetClusterRef.Namespace,
	}
	// Use s.Client (client for the main/host cluster) to get your Cluster CR
	if err := s.Client.Get(ctx, clusterCRKey, clusterCR); err != nil {
		logger.Error(err, "Failed to get target Cluster CR", "targetClusterRef", s.Config.TargetClusterRef)
		// You might want to update the SyncRule status with this error.
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, fmt.Errorf("failed to get target Cluster CR %v: %w", clusterCRKey, err)
	}

	// Placeholder for your kubeconfig extraction and client creation
	// endpoint := server.ServiceName(clusterCR.Name) + "." + clusterCR.Namespace // Adapt as needed
	// kubeCfg, err := kubeconfig.New().Extract(ctx, s.Client, clusterCR, endpoint) // Adapt as needed
	// if err != nil {
	// 	logger.Error(err, "Failed to extract kubeconfig for target cluster")
	// 	return ctrl.Result{}, err
	// }
	// kubeCfg.Clusters[kubeCfg.CurrentContext].Server = "https://" + endpoint // Adapt as needed
	// clientConfig := clientcmd.NewDefaultClientConfig(*kubeCfg, &clientcmd.ConfigOverrides{})
	// restConfig, err := clientConfig.ClientConfig()
	// if err != nil {
	// 	logger.Error(err, "Failed to create REST config for target cluster")
	// 	return ctrl.Result{}, err
	// }
	// targetK8sClient, err := client.New(restConfig, client.Options{Scheme: s.Scheme})
	// if err != nil {
	// 	logger.Error(err, "Failed to create client for target cluster")
	// 	return ctrl.Result{}, err
	// }
	// --- End Placeholder for Target Client ---
	// For now, let's assume targetK8sClient is magically available and is s.Client for testing on same cluster
	targetK8sClient := s.Client // !!! REPLACE WITH REAL TARGET CLIENT LOGIC !!!
	if targetK8sClient == nil { // Should be checked after real client creation
		err := fmt.Errorf("target Kubernetes client is nil, cannot proceed")
		logger.Error(err, "Target client not initialized")
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, err
	}

	// --- 2. Get Source Resource ---
	sourceResource := &unstructured.Unstructured{}
	sourceResource.SetGroupVersionKind(s.Config.SourceGVK)

	// Determine which namespace to use for Get based on SyncRule
	getSourceNamespace := s.Config.SourceNamespace
	if s.Config.SourceGVK.Kind == "Namespace" || s.Config.SourceGVK.Kind == "ClusterRole" || s.Config.SourceGVK.Kind == "ClusterRoleBinding" || s.Config.SourceGVK.Kind == "PriorityClass" || s.Config.SourceGVK.Kind == "ClusterIssuer" { // Add other cluster-scoped kinds
		getSourceNamespace = "" // For cluster-scoped resources, req.Namespace will be empty if CR is cluster-scoped.
	} else if getSourceNamespace == "" && req.Namespace != "" { // if rule doesn't specify namespace, use object's namespace
		getSourceNamespace = req.Namespace
	}

	sourceObjectKey := client.ObjectKey{Name: req.Name, Namespace: getSourceNamespace}
	if getSourceNamespace == "" { // For cluster-scoped resources from a cluster-scoped request
		sourceObjectKey.Namespace = ""
	}

	if err := s.Client.Get(ctx, sourceObjectKey, sourceResource); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Source resource not found. Attempting to delete target.", "source", sourceObjectKey)
			// --- Handle Deletion of Target Resource ---
			targetToDelete := &unstructured.Unstructured{}
			targetToDelete.SetGroupVersionKind(s.Config.SourceGVK) // Assuming target GVK is same as source
			targetToDelete.SetName(req.Name)
			// Determine target namespace
			deleteTargetNamespace := s.Config.TargetNamespace
			if s.Config.SourceGVK.Kind == "Namespace" || s.Config.SourceGVK.Kind == "ClusterRole" || s.Config.SourceGVK.Kind == "ClusterRoleBinding" || s.Config.SourceGVK.Kind == "PriorityClass" || s.Config.SourceGVK.Kind == "ClusterIssuer" { // Add other cluster-scoped kinds
				deleteTargetNamespace = ""
			} else if deleteTargetNamespace == "" { // If target ns not specified in rule, use source ns
				deleteTargetNamespace = getSourceNamespace // or req.Namespace
			}

			if deleteTargetNamespace != "" {
				targetToDelete.SetNamespace(deleteTargetNamespace)
			}

			if errDel := targetK8sClient.Delete(ctx, targetToDelete); errDel != nil && !errors.IsNotFound(errDel) {
				logger.Error(errDel, "Failed to delete target resource", "target", client.ObjectKeyFromObject(targetToDelete))
				return ctrl.Result{}, errDel
			}
			logger.Info("Target resource deleted or was not found", "target", client.ObjectKeyFromObject(targetToDelete))
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get source resource", "source", sourceObjectKey)
		return ctrl.Result{}, err
	}
	logger.Info("Successfully fetched source resource", "source", sourceObjectKey)

	// --- 3. CreateOrUpdate Target Resource ---
	opCtx, opCancel := context.WithTimeout(ctx, 30*time.Second) // Longer timeout for cross-cluster
	defer opCancel()

	targetResource := &unstructured.Unstructured{}
	targetResource.SetGroupVersionKind(s.Config.SourceGVK) // Assuming target GVK is same as source
	targetResource.SetName(sourceResource.GetName())

	// Determine target namespace
	applyTargetNamespace := s.Config.TargetNamespace
	if s.Config.SourceGVK.Kind == "Namespace" || s.Config.SourceGVK.Kind == "ClusterRole" || s.Config.SourceGVK.Kind == "ClusterRoleBinding" || s.Config.SourceGVK.Kind == "PriorityClass" || s.Config.SourceGVK.Kind == "ClusterIssuer" { // Add other cluster-scoped kinds
		applyTargetNamespace = ""
	} else if applyTargetNamespace == "" { // If target ns not specified in rule, use source ns
		applyTargetNamespace = sourceResource.GetNamespace()
	}

	if applyTargetNamespace != "" {
		targetResource.SetNamespace(applyTargetNamespace)
	}

	operationResult, err := controllerutil.CreateOrUpdate(opCtx, targetK8sClient, targetResource, func() error {
		// Ensure GVK, Name, Namespace are correctly set in the mutateFn for the target
		targetResource.SetGroupVersionKind(s.Config.SourceGVK) // Use target GVK from config if different
		targetResource.SetName(sourceResource.GetName())       // Apply name transformations if any
		if applyTargetNamespace != "" {
			targetResource.SetNamespace(applyTargetNamespace) // Apply namespace transformations if any
		} else {
			targetResource.SetNamespace("") // Ensure it's empty for cluster-scoped
		}

		// Simple copy - apply transformations here in a real version
		targetResource.SetLabels(sourceResource.GetLabels())
		targetResource.SetAnnotations(filterMetadata(sourceResource.GetAnnotations())) // Filter some common problematic annotations

		spec, found, specErr := unstructured.NestedMap(sourceResource.Object, "spec")
		if specErr != nil {
			return fmt.Errorf("error getting spec from source: %w", specErr)
		}

		if found {
			if err := unstructured.SetNestedMap(targetResource.Object, spec, "spec"); err != nil {
				return fmt.Errorf("error setting spec for target: %w", err)
			}
		} else {
			delete(targetResource.Object, "spec") // Ensure spec is removed if not present in source
		}
		return nil
	})

	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate target resource", "target", client.ObjectKeyFromObject(targetResource), "operationResult", operationResult)
		// Update SyncRule status with this error
		return ctrl.Result{}, err
	}
	logger.Info("Target resource sync success", "target", client.ObjectKeyFromObject(targetResource), "result", operationResult)

	// --- 4. Status Sync (Optional, e.g., Source -> Target, as in your original example) ---
	// This is a basic example. Robust status sync is complex.
	sourceStatus, sourceStatusExists, _ := unstructured.NestedMap(sourceResource.Object, "status")
	if sourceStatusExists {
		// Get the just created/updated target resource to ensure we have its latest state
		// before attempting a status update on it.
		updatedTargetForStatus := &unstructured.Unstructured{}

		// Set the GroupVersionKind on 'updatedTargetForStatus' so the Get call knows what to fetch.
		// This should be the GVK of the target resource. In our simplified example,
		// we assumed the target GVK is the same as the source GVK defined in the config.
		// If you had a specific TargetGVK in your s.Config, you'd use that.
		updatedTargetForStatus.SetGroupVersionKind(s.Config.SourceGVK) // Or s.Config.TargetGVK if you define one

		// Use client.ObjectKeyFromObject(targetResource) to get the Name and Namespace
		// of the object that was just successfully created or updated.
		targetKey := client.ObjectKeyFromObject(targetResource)

		logger.V(1).Info("Attempting to re-fetch target resource for status update", "targetKey", targetKey, "gvk", updatedTargetForStatus.GroupVersionKind())

		if errGetTarget := targetK8sClient.Get(opCtx, targetKey, updatedTargetForStatus); errGetTarget == nil {
			currentTargetStatus, _, _ := unstructured.NestedMap(updatedTargetForStatus.Object, "status")
			if !reflect.DeepEqual(sourceStatus, currentTargetStatus) {
				logger.Info("Attempting to sync status from source to target", "target", targetKey)
				if errSetStatus := unstructured.SetNestedMap(updatedTargetForStatus.Object, sourceStatus, "status"); errSetStatus != nil {
					logger.Error(errSetStatus, "Failed to set status on target object model for update", "target", targetKey)
				} else {
					if errUpdateStatus := targetK8sClient.Status().Update(opCtx, updatedTargetForStatus); errUpdateStatus != nil {
						logger.Error(errUpdateStatus, "Failed to update target resource status", "target", targetKey)
						// Decide if this error should cause a requeue or just be logged
					} else {
						logger.Info("Target resource status updated from source", "target", targetKey)
					}
				}
			} else {
				logger.V(1).Info("Target status already matches source status or source has no status to sync.", "target", targetKey)
			}
		} else {
			logger.Error(errGetTarget, "Failed to get target resource immediately after CreateOrUpdate for status sync", "target", targetKey)
			// If you can't get the target, you probably can't update its status.
			// You might want to requeue or log this as a transient issue.
		}
	}

	// Update SyncRule status to "Synced"
	return ctrl.Result{}, nil
}

// filterMetadata removes common annotations that shouldn't be copied.
func filterMetadata(annotations map[string]string) map[string]string {
	if annotations == nil {
		return nil
	}
	filtered := make(map[string]string)
	denylist := []string{
		"kubectl.kubernetes.io/last-applied-configuration",
		// Add other annotations typically managed by controllers or system that shouldn't be blindly copied
	}
	for k, v := range annotations {
		isDenied := false
		for _, deniedKey := range denylist {
			if k == deniedKey {
				isDenied = true
				break
			}
		}
		if !isDenied {
			filtered[k] = v
		}
	}
	return filtered
}
