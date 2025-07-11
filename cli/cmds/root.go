package cmds

import (
	"context"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/rancher/k3k/pkg/buildinfo"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type AppContext struct {
	RestConfig *rest.Config
	Client     client.Client

	// Global flags
	Debug      bool
	Kubeconfig string
	namespace  string
}

func NewApp() *cli.Command {
	appCtx := &AppContext{}

	app := &cli.Command{
		EnableShellCompletion: true,
		Name:                  "k3kcli",
		Usage:                 "CLI for K3K",
		Version:               buildinfo.Version,
		Flags:                 CommonFlags(appCtx),
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			if appCtx.Debug {
				logrus.SetLevel(logrus.DebugLevel)
			}

			restConfig, err := loadRESTConfig(appCtx.Kubeconfig)
			if err != nil {
				return ctx, err
			}

			scheme := runtime.NewScheme()
			_ = clientgoscheme.AddToScheme(scheme)
			_ = v1alpha1.AddToScheme(scheme)
			_ = apiextensionsv1.AddToScheme(scheme)

			ctrlClient, err := client.New(restConfig, client.Options{Scheme: scheme})
			if err != nil {
				return ctx, err
			}

			appCtx.RestConfig = restConfig
			appCtx.Client = ctrlClient

			return ctx, nil
		},
		Commands: []*cli.Command{
			NewClusterCmd(appCtx),
			NewPolicyCmd(appCtx),
			NewKubeconfigCmd(appCtx),
		},
	}

	return app
}

func (ctx *AppContext) Namespace(name string) string {
	if ctx.namespace != "" {
		return ctx.namespace
	}

	return "k3k-" + name
}

func loadRESTConfig(kubeconfig string) (*rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}

	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	return kubeConfig.ClientConfig()
}

func CommonFlags(appCtx *AppContext) []cli.Flag {
	return []cli.Flag{
		FlagDebug(appCtx),
		FlagKubeconfig(appCtx),
	}
}

func FlagDebug(appCtx *AppContext) *cli.BoolFlag {
	return &cli.BoolFlag{
		Name:        "debug",
		Usage:       "Turn on debug logs",
		Destination: &appCtx.Debug,
		Sources:     cli.EnvVars("K3K_DEBUG"),
	}
}

func FlagKubeconfig(appCtx *AppContext) *cli.StringFlag {
	return &cli.StringFlag{
		Name:        "kubeconfig",
		Usage:       "kubeconfig path",
		Destination: &appCtx.Kubeconfig,
		DefaultText: "$HOME/.kube/config or $KUBECONFIG if set",
	}
}

func FlagNamespace(appCtx *AppContext) *cli.StringFlag {
	return &cli.StringFlag{
		Name:        "namespace",
		Usage:       "namespace of the k3k cluster",
		Aliases:     []string{"n"},
		Destination: &appCtx.namespace,
	}
}
