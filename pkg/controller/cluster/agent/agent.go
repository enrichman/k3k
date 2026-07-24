package agent

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
	"github.com/rancher/k3k/pkg/controller"
)

const (
	configName = "agent-config"
)

type ResourceEnsurer interface {
	EnsureResources(context.Context) error
}

type Config struct {
	cluster *v1beta1.Cluster
	client  client.Client
	scheme  *runtime.Scheme
}

func NewConfig(cluster *v1beta1.Cluster, client client.Client) *Config {
	return &Config{
		cluster: cluster,
		client:  client,
		scheme:  client.Scheme(),
	}
}

func configSecretName(clusterName string) string {
	return controller.SafeConcatNameWithPrefix(clusterName, configName)
}

func ensureObject(ctx context.Context, cfg *Config, obj client.Object) error {
	key := client.ObjectKeyFromObject(obj)
	log := ctrl.LoggerFrom(ctx).WithValues("key", key)

	if err := controllerutil.SetControllerReference(cfg.cluster, obj, cfg.scheme); err != nil {
		return err
	}

	if err := cfg.client.Create(ctx, obj); err != nil {
		if apierrors.IsAlreadyExists(err) {
			log.V(1).Info(fmt.Sprintf("Resource %T already exists, updating.", obj))

			return cfg.client.Update(ctx, obj)
		}

		return err
	}

	log.V(1).Info(fmt.Sprintf("Creating %T.", obj))

	return nil
}
