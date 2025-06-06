package generic

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// SyncRuleReconciler reconciles a SyncRule object
type SyncRuleReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

type DynamicSyncerConfig struct {
	RuleNamespacedName types.NamespacedName
	SourceGVK          schema.GroupVersionKind
	SourceNamespace    string
	TargetClusterRef   v1alpha1.TargetClusterRef
	TargetNamespace    string
}

type DynamicResourceSyncer struct {
	client.Client
	Scheme *runtime.Scheme
	Config DynamicSyncerConfig
}

func SetupWithManager(mgr ctrl.Manager) error {
	r := &SyncRuleReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.SyncRule{}).
		Complete(r)
}

func (r *SyncRuleReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("syncrule", req.NamespacedName)

	var syncRule v1alpha1.SyncRule
	if err := r.Get(ctx, req.NamespacedName, &syncRule); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("SyncRule not found")

			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, fmt.Errorf("failed to get SyncRule: %w", err)
	}

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
	controllerName := fmt.Sprintf("syncer-%s-%s-%s-%s",
		strings.ReplaceAll(config.SourceGVK.Group, ".", "-"),
		config.SourceGVK.Version,
		config.SourceGVK.Kind,
		strings.ReplaceAll(ruleKey.String(), "/", "-"),
	)
	controllerName = strings.ToLower(controllerName)
	controllerName = fmt.Sprintf("%s-%d", controllerName, time.Now().Unix())

	logger := log.FromContext(ctx).WithValues("dynamicController", controllerName, "rule", ruleKey)

	dynamicSyncer := &DynamicResourceSyncer{
		Client: r.Client,
		Scheme: r.Scheme,
		Config: config,
	}

	logger.Info("Creating dynamic controller", "controllerName", controllerName)

	// creates a new manager (??)
	restConfig, err := clientcmd.BuildConfigFromFlags("", "")
	if err != nil {
		return fmt.Errorf("failed to create config from kubeconfig file: %v", err)
	}

	mgr, err := ctrl.NewManager(restConfig, manager.Options{
		Scheme: r.Scheme,
	})
	if err != nil {
		return fmt.Errorf("failed to create config from kubeconfig file: %v", err)
	}

	// Create a new controller instance. This controller has its own workqueue.
	dynCtrl, err := controller.New(controllerName, mgr, controller.Options{
		Reconciler: dynamicSyncer,
	})
	if err != nil {
		return fmt.Errorf("failed to create new controller runtime manager: %v", err)
	}

	sourceObjToWatch := &unstructured.Unstructured{}
	sourceObjToWatch.SetGroupVersionKind(config.SourceGVK)

	src := source.Kind(
		mgr.GetCache(),
		sourceObjToWatch,
		&handler.TypedEnqueueRequestForObject[*unstructured.Unstructured]{},
	)

	//src := source.Kind(mgr.GetCache(), sourceObjToWatch, handler.TypedEnqueueRequestForObject[unstructured.Unstructured]{})
	if err := dynCtrl.Watch(src); err != nil {
		return fmt.Errorf("failed to set up watch for dynamic controller %s: %w", controllerName, err)
	}

	go func() {
		logger.Info("Starting dynamic controller goroutine", "controllerName", controllerName)
		if err := dynCtrl.Start(context.Background()); err != nil {
			logger.Error(err, "Dynamic controller returned an error", "controllerName", controllerName)
		}

		logger.Info("Stopped dynamic controller goroutine", "controllerName", controllerName)
	}()

	return nil
}

func (s *DynamicResourceSyncer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"dynamicSyncerForRule", s.Config.RuleNamespacedName,
		"sourceObject", req.NamespacedName,
		"sourceGVK", s.Config.SourceGVK,
	)
	logger.Info("DynamicResourceSyncer processing request")

	var targetK8sClient client.Client

	// TODO
	clusterCR := &v1alpha1.Cluster{}

	clusterCRKey := types.NamespacedName{
		Name:      s.Config.TargetClusterRef.Name,
		Namespace: s.Config.TargetClusterRef.Namespace,
	}

	if err := s.Client.Get(ctx, clusterCRKey, clusterCR); err != nil {
		logger.Error(err, "Failed to get target Cluster CR", "targetClusterRef", s.Config.TargetClusterRef)
		// TODO: Update SyncRule status
		return ctrl.Result{}, fmt.Errorf("failed to get target Cluster CR %v: %w", clusterCRKey, err)
	}

	if targetK8sClient == nil { // This check should be after your client creation logic
		targetK8sClient = s.Client // TEMPORARY FALLBACK FOR TESTING - REMOVE FOR PRODUCTION
		logger.Info("WARNING: Target K8s client was nil, falling back to source client for testing. REPLACE THIS.")
	}

	sourceResource := &unstructured.Unstructured{}
	sourceResource.SetGroupVersionKind(s.Config.SourceGVK)

	getSourceNamespace := s.Config.SourceNamespace

	if isClusterScoped(s.Config.SourceGVK.Kind) {
		getSourceNamespace = ""
	} else if getSourceNamespace == "" { // If rule allows watching all namespaces for a namespaced kind
		getSourceNamespace = req.Namespace // Use the namespace from the event
	}

	sourceObjectKey := client.ObjectKey{Name: req.Name, Namespace: getSourceNamespace}

	if err := s.Client.Get(ctx, sourceObjectKey, sourceResource); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Source resource not found. Attempting to delete target.", "source", sourceObjectKey)

			targetToDelete := &unstructured.Unstructured{}
			// Assuming target GVK is same as source for this simpler spec
			targetToDelete.SetGroupVersionKind(s.Config.SourceGVK)
			targetToDelete.SetName(req.Name)

			deleteTargetNamespace := s.Config.TargetNamespace
			if isClusterScoped(s.Config.SourceGVK.Kind) {
				deleteTargetNamespace = ""
			} else if deleteTargetNamespace == "" {
				deleteTargetNamespace = getSourceNamespace // Default to source's namespace if not specified
			}

			if deleteTargetNamespace != "" {
				targetToDelete.SetNamespace(deleteTargetNamespace)
			}

			if errDel := targetK8sClient.Delete(ctx, targetToDelete, client.PropagationPolicy(metav1.DeletePropagationBackground)); errDel != nil && !errors.IsNotFound(errDel) {
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

	targetResource := &unstructured.Unstructured{}
	targetResource.SetGroupVersionKind(s.Config.SourceGVK)
	targetResource.SetName(sourceResource.GetName()) // Default: same name. Apply transformations if any.

	applyTargetNamespace := s.Config.TargetNamespace
	if isClusterScoped(s.Config.SourceGVK.Kind) {
		applyTargetNamespace = ""
	} else if applyTargetNamespace == "" {
		applyTargetNamespace = sourceResource.GetNamespace() // Default to source's namespace if not specified
	}

	if applyTargetNamespace != "" {
		targetResource.SetNamespace(applyTargetNamespace)
	}

	operationResult, err := controllerutil.CreateOrUpdate(ctx, targetK8sClient, targetResource, func() error {
		targetResource.SetGroupVersionKind(s.Config.SourceGVK) // Target GVK from config, could differ from source
		targetResource.SetName(sourceResource.GetName())
		if applyTargetNamespace != "" {
			targetResource.SetNamespace(applyTargetNamespace)
		} else {
			targetResource.SetNamespace("") // Important for cluster-scoped resources
		}

		targetResource.SetLabels(sourceResource.GetLabels())                           // TODO: Add transformation logic from SyncRule
		targetResource.SetAnnotations(filterMetadata(sourceResource.GetAnnotations())) // TODO: Add transformation logic

		spec, found, specErr := unstructured.NestedMap(sourceResource.Object, "spec")
		if specErr != nil {
			return fmt.Errorf("error getting spec from source: %w", specErr)
		}
		if found {
			if err := unstructured.SetNestedMap(targetResource.Object, spec, "spec"); err != nil { // TODO: Add transformation logic
				return fmt.Errorf("error setting spec for target: %w", err)
			}
		} else {
			delete(targetResource.Object, "spec")
		}
		return nil
	})

	if err != nil {
		logger.Error(err, "Failed to CreateOrUpdate target resource", "target", client.ObjectKeyFromObject(targetResource), "operationResult", operationResult)
		return ctrl.Result{}, err
	}
	logger.Info("Target resource sync success", "target", client.ObjectKeyFromObject(targetResource), "result", operationResult)

	// --- 4. Status Sync (Optional, e.g., Source -> Target) ---
	sourceStatus, sourceStatusExists, _ := unstructured.NestedMap(sourceResource.Object, "status")
	if sourceStatusExists {
		updatedTargetForStatus := &unstructured.Unstructured{}
		updatedTargetForStatus.SetGroupVersionKind(s.Config.SourceGVK) // Target GVK

		targetKey := client.ObjectKeyFromObject(targetResource)
		if errGetTarget := targetK8sClient.Get(ctx, targetKey, updatedTargetForStatus); errGetTarget == nil {
			currentTargetStatus, _, _ := unstructured.NestedMap(updatedTargetForStatus.Object, "status")
			if !reflect.DeepEqual(sourceStatus, currentTargetStatus) {
				logger.Info("Attempting to sync status from source to target", "target", targetKey)
				if errSetStatus := unstructured.SetNestedMap(updatedTargetForStatus.Object, sourceStatus, "status"); errSetStatus != nil {
					logger.Error(errSetStatus, "Failed to set status on target object for update")
				} else {
					if errUpdateStatus := targetK8sClient.Status().Update(ctx, updatedTargetForStatus); errUpdateStatus != nil {
						logger.Error(errUpdateStatus, "Failed to update target resource status")
					} else {
						logger.Info("Target resource status updated from source")
					}
				}
			}
		} else {
			logger.Error(errGetTarget, "Failed to get target after C/U for status sync", "target", targetKey)
		}
	}
	// Update SyncRule status (e.g., LastSyncTime, Synced Condition) via s.Client
	return ctrl.Result{}, nil
}

// isClusterScoped is a helper, you'll need a more robust way for real (e.g. API discovery)
func isClusterScoped(kind string) bool {
	clusterScopedKinds := map[string]bool{
		"Namespace":          true,
		"Node":               true,
		"PersistentVolume":   true,
		"ClusterRole":        true,
		"ClusterRoleBinding": true,
		"ClusterIssuer":      true, // from cert-manager
		"PriorityClass":      true,
		// Add other known cluster-scoped Kinds your controller might handle
	}
	return clusterScopedKinds[kind]
}

// filterMetadata removes common annotations that shouldn't be copied.
func filterMetadata(annotations map[string]string) map[string]string {
	if annotations == nil {
		return nil
	}
	filtered := make(map[string]string)
	// Consider making this denylist configurable via SyncRule or controller config
	denylist := []string{
		"kubectl.kubernetes.io/last-applied-configuration",
	}
	for k, v := range annotations {
		isDenied := false
		for _, deniedKey := range denylist {
			if strings.HasPrefix(k, deniedKey) { // Use HasPrefix for broader matches like controller specific annotations
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
