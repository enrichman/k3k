package generic

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/rancher/k3k/pkg/controller/cluster/server"
	"github.com/rancher/k3k/pkg/controller/kubeconfig"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// DynamicController represents a running dynamic controller instance
type DynamicController struct {
	Name    string
	Context context.Context
	Cancel  context.CancelFunc
	Manager manager.Manager
	Started bool
	mutex   sync.RWMutex
}

type DynamicSyncerConfig struct {
	RuleNamespacedName types.NamespacedName
	SourceGVK          schema.GroupVersionKind
	SourceNamespace    string
	TargetClusterRef   v1alpha1.TargetClusterRef
	TargetNamespace    string
	Workqueue          workqueue.TypedRateLimitingInterface[reconcile.Request] // <-- ADD THIS
}

type DynamicResourceSyncer struct {
	client.Client
	Scheme *runtime.Scheme
	Config DynamicSyncerConfig
}

func (s *DynamicResourceSyncer) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"dynamicSyncerForRule", s.Config.RuleNamespacedName,
		"sourceObject", req.NamespacedName,
		"sourceGVK", s.Config.SourceGVK,
	)
	logger.Info("DynamicResourceSyncer processing request")

	opCtx, opCancel := context.WithTimeout(ctx, 30*time.Second)
	defer opCancel()

	// start create client

	cluster := &v1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mycluster",
			Namespace: "k3k-mycluster"},
	}

	key := client.ObjectKeyFromObject(cluster)

	if err := s.Client.Get(ctx, client.ObjectKeyFromObject(cluster), cluster); err != nil {
		logger.Error(err, "Failed to get target Cluster CR", "targetClusterRef", s.Config.TargetClusterRef)
		// TODO: Update SyncRule status
		return ctrl.Result{}, fmt.Errorf("failed to get target Cluster CR %v: %w", key, err)
	}

	endpoint := server.ServiceName(cluster.Name) + "." + cluster.Namespace

	kubeCfg, err := kubeconfig.New().Extract(ctx, s.Client, cluster, endpoint)
	if err != nil {
		return reconcile.Result{}, err
	}

	kubeCfg.Clusters[kubeCfg.CurrentContext].Server = "https://" + endpoint

	clientConfig := clientcmd.NewDefaultClientConfig(*kubeCfg, &clientcmd.ConfigOverrides{})

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return reconcile.Result{}, err
	}

	targetK8sClient, err := client.New(restConfig, client.Options{Scheme: s.Scheme})
	if err != nil {
		return reconcile.Result{}, err
	}

	// end

	sourceResource := &unstructured.Unstructured{}
	sourceResource.SetGroupVersionKind(s.Config.SourceGVK)

	if err := s.Client.Get(ctx, req.NamespacedName, sourceResource); err != nil {
		if kerrors.IsNotFound(err) {
			logger.Info("Source resource not found. Attempting to delete target.")

			//TODO

			return ctrl.Result{}, nil
		}

		logger.Error(err, "Failed to get source resource")

		return ctrl.Result{}, err
	}

	logger.Info("Successfully fetched source resource")

	targetResource := &unstructured.Unstructured{}
	targetResource.SetGroupVersionKind(s.Config.SourceGVK)
	targetResource.SetName(sourceResource.GetName())
	targetResource.SetNamespace(sourceResource.GetNamespace())

	operationResult, err := controllerutil.CreateOrUpdate(opCtx, targetK8sClient, targetResource, func() error {
		targetResource.SetGroupVersionKind(s.Config.SourceGVK) // Target GVK from config, could differ from source
		targetResource.SetName(sourceResource.GetName())
		targetResource.SetNamespace(sourceResource.GetNamespace())

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

		// A. Check if the error indicates the Kind is not registered on the target cluster.
		if meta.IsNoMatchError(err) {
			logger.Info("Target resource Kind is not recognized. Attempting to sync its CRD.", "gvk", s.Config.SourceGVK)

			// B. Attempt to find and copy the CRD from the source to the target.
			errSyncCRD := s.syncCRD(ctx, opCtx, targetK8sClient)
			if errSyncCRD != nil {
				logger.Error(errSyncCRD, "Failed to sync CRD.", "gvk", s.Config.SourceGVK)
				// Return the original error, as we failed to fix the problem.
				return ctrl.Result{}, err
			}

			// C. Requeue the reconciliation.
			// Give the target API server a moment to register the newly created CRD.
			logger.Info("CRD sync successful. Requeuing original request to retry.", "requeueAfter", "2s")

			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}

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

// syncCRD finds the CRD for the source GVK on the source cluster and creates it on the target cluster.
func (s *DynamicResourceSyncer) syncCRD(ctx context.Context, opCtx context.Context, targetK8sClient client.Client) error {
	logger := log.FromContext(ctx).WithValues("rule", s.Config.RuleNamespacedName)

	if s.Config.SourceGVK.Group == "" { // Core types don't have CRDs.
		return fmt.Errorf("cannot sync CRD for core type with empty group: %s", s.Config.SourceGVK.Kind)
	}

	// 1. List all CustomResourceDefinitions from the source cluster.
	logger.Info("Listing all CRDs from source cluster to find match", "gvk", s.Config.SourceGVK)

	crdList := &apiextensionsv1.CustomResourceDefinitionList{}
	if err := s.Client.List(ctx, crdList); err != nil {
		return fmt.Errorf("failed to list source CRDs: %w", err)
	}

	// 2. Find the specific CRD that matches our source GVK.
	var sourceCRD *apiextensionsv1.CustomResourceDefinition

	for i := range crdList.Items {
		crd := &crdList.Items[i]
		// Match on both Group and Kind.
		if crd.Spec.Group == s.Config.SourceGVK.Group && crd.Spec.Names.Kind == s.Config.SourceGVK.Kind {
			sourceCRD = crd
			break
		}
	}

	if sourceCRD == nil {
		return fmt.Errorf("could not find source CRD for GVK %v on the source cluster", s.Config.SourceGVK)
	}

	logger.Info("Successfully found matching source CRD", "crdName", sourceCRD.Name)

	// 3. Create a clean CRD object for the target cluster.
	targetCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:        sourceCRD.Name,
			Labels:      sourceCRD.Labels,
			Annotations: sourceCRD.Annotations,
		},
		Spec: sourceCRD.Spec, // The Spec defines the resource and is what we need to copy.
	}

	// 4. Create the CRD on the target cluster.
	logger.Info("Attempting to create CRD on target cluster", "crdName", targetCRD.Name)

	if err := targetK8sClient.Create(opCtx, targetCRD); err != nil {
		if kerrors.IsAlreadyExists(err) {
			logger.Info("CRD already exists on target cluster.", "crdName", targetCRD.Name)
			return nil // Success, we can proceed with the retry.
		}

		return fmt.Errorf("failed to create CRD %s on target cluster: %w", targetCRD.Name, err)
	}

	logger.Info("Successfully created CRD on target cluster", "crdName", targetCRD.Name)

	return nil
}

// processNextWorkItem is the standard worker loop logic
func (s *DynamicResourceSyncer) processNextWorkItem(ctx context.Context, logger logr.Logger) bool {
	req, shutdown := s.Config.Workqueue.Get()
	if shutdown {
		return false // Queue is shutting down
	}
	defer s.Config.Workqueue.Done(req)

	result, err := s.Reconcile(ctx, req)
	if err != nil {
		s.Config.Workqueue.AddRateLimited(req)
		logger.Error(err, "Reconciliation failed, item requeued", "request", req)

		return true
	}

	if result.RequeueAfter > 0 {
		s.Config.Workqueue.AddAfter(req, result.RequeueAfter)
		logger.V(1).Info("Reconciliation requested requeue after delay", "request", req, "delay", result.RequeueAfter.String())

		return true
	}

	if result.Requeue {
		s.Config.Workqueue.AddRateLimited(req)
		logger.V(1).Info("Reconciliation requested immediate requeue", "request", req)

		return true
	}

	// Forget the item on success, so we don't retry it.
	s.Config.Workqueue.Forget(req)
	logger.Info("Successfully reconciled", "request", req)

	return true // Return true to continue the worker loop
}
