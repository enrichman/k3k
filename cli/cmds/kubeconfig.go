package cmds

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/rancher/k3k/pkg/controller"
	"github.com/rancher/k3k/pkg/controller/certs"
	"github.com/rancher/k3k/pkg/controller/kubeconfig"
)

type GenerateKubeconfigConfig struct {
	name                 string
	configName           string
	cn                   string
	org                  []string
	altNames             []string
	expirationDays       int64
	kubeconfigServerHost string
}

func NewKubeconfigCmd(appCtx *AppContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kubeconfig",
		Short: "Manage kubeconfig for clusters",
	}

	cmd.AddCommand(
		NewKubeconfigGenerateCmd(appCtx),
	)

	return cmd
}

func NewKubeconfigGenerateCmd(appCtx *AppContext) *cobra.Command {
	cfg := &GenerateKubeconfigConfig{}

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate kubeconfig for clusters",
		RunE:  generate(appCtx, cfg),
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			client := appCtx.Client

			var clusterList v1alpha1.ClusterList
			if err := client.List(context.Background(), &clusterList); err != nil {
				return nil, cobra.ShellCompDirectiveError
			}

			var clusterNames []string
			for _, cluster := range clusterList.Items {
				nsName := fmt.Sprintf("%s/%s", cluster.Namespace, cluster.Name)

				if strings.HasPrefix(nsName, toComplete) {
					clusterNames = append(clusterNames, nsName)
				}
			}

			var filtered []string

			for _, clusterName := range clusterNames {
				if !slices.Contains(args, clusterName) {
					filtered = append(filtered, clusterName)
				}
			}

			return filtered, cobra.ShellCompDirectiveNoFileComp
		},
	}

	CobraFlagNamespace(appCtx, cmd.Flags())
	generateKubeconfigFlags(cmd, cfg)

	return cmd
}

func generateKubeconfigFlags(cmd *cobra.Command, cfg *GenerateKubeconfigConfig) {
	cmd.Flags().StringVar(&cfg.name, "name", "", "cluster name")
	cmd.Flags().StringVar(&cfg.configName, "config-name", "", "the name of the generated kubeconfig file")
	cmd.Flags().StringVar(&cfg.cn, "cn", controller.AdminCommonName, "Common name (CN) of the generated certificates for the kubeconfig")
	cmd.Flags().StringSliceVar(&cfg.org, "org", nil, "Organization name (ORG) of the generated certificates for the kubeconfig")
	cmd.Flags().StringSliceVar(&cfg.altNames, "altNames", nil, "altNames of the generated certificates for the kubeconfig")
	cmd.Flags().Int64Var(&cfg.expirationDays, "expiration-days", 365, "Expiration date of the certificates used for the kubeconfig")
	cmd.Flags().StringVar(&cfg.kubeconfigServerHost, "kubeconfig-server", "", "override the kubeconfig server host")
}

func generate(appCtx *AppContext, cfg *GenerateKubeconfigConfig) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		nsName := strings.Split(args[0], "/")

		ctx := context.Background()
		client := appCtx.Client

		clusterKey := types.NamespacedName{
			Namespace: nsName[0],
			Name:      nsName[1],
		}

		var cluster v1alpha1.Cluster
		if err := client.Get(ctx, clusterKey, &cluster); err != nil {
			return err
		}

		url, err := url.Parse(appCtx.RestConfig.Host)
		if err != nil {
			return err
		}

		host := strings.Split(url.Host, ":")
		if cfg.kubeconfigServerHost != "" {
			host = []string{cfg.kubeconfigServerHost}
			cfg.altNames = append(cfg.altNames, cfg.kubeconfigServerHost)
		}

		certAltNames := certs.AddSANs(cfg.altNames)

		if len(cfg.org) == 0 {
			cfg.org = []string{user.SystemPrivilegedGroup}
		}

		kubeCfg := kubeconfig.KubeConfig{
			CN:         cfg.cn,
			ORG:        cfg.org,
			ExpiryDate: time.Hour * 24 * time.Duration(cfg.expirationDays),
			AltNames:   certAltNames,
		}

		logrus.Infof("waiting for cluster to be available..")

		var kubeconfig *clientcmdapi.Config

		if err := retry.OnError(controller.Backoff, apierrors.IsNotFound, func() error {
			kubeconfig, err = kubeCfg.Generate(ctx, client, &cluster, host[0], 0)
			return err
		}); err != nil {
			return err
		}

		return writeKubeconfigFile(&cluster, kubeconfig, cfg.configName)
	}
}

func writeKubeconfigFile(cluster *v1alpha1.Cluster, kubeconfig *clientcmdapi.Config, configName string) error {
	if configName == "" {
		configName = cluster.Namespace + "-" + cluster.Name + "-kubeconfig.yaml"
	}

	pwd, err := os.Getwd()
	if err != nil {
		return err
	}

	logrus.Infof(`You can start using the cluster with:

	export KUBECONFIG=%s
	kubectl cluster-info
	`, filepath.Join(pwd, configName))

	kubeconfigData, err := clientcmd.Write(*kubeconfig)
	if err != nil {
		return err
	}

	return os.WriteFile(configName, kubeconfigData, 0o644)
}
