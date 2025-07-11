package main

import (
	"context"
	"os"

	"github.com/go-logr/zapr"
	"github.com/rancher/k3k/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	"go.uber.org/zap"
	ctrlruntimelog "sigs.k8s.io/controller-runtime/pkg/log"
)

var (
	configFile string
	cfg        config
	logger     *log.Logger
	debug      bool
)

func main() {
	app := newApp()
	if err := app.Run(context.Background(), os.Args); err != nil {
		logrus.Fatal(err)
	}
}

func newApp() *cli.Command {
	return &cli.Command{
		Name:  "k3k-kubelet",
		Usage: "virtual kubelet implementation k3k",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "cluster-name",
				Usage:       "Name of the k3k cluster",
				Destination: &cfg.ClusterName,
				Sources:     cli.EnvVars("CLUSTER_NAME"),
			},
			&cli.StringFlag{
				Name:        "cluster-namespace",
				Usage:       "Namespace of the k3k cluster",
				Destination: &cfg.ClusterNamespace,
				Sources:     cli.EnvVars("CLUSTER_NAMESPACE"),
			},
			&cli.StringFlag{
				Name:        "cluster-token",
				Usage:       "K3S token of the k3k cluster",
				Destination: &cfg.Token,
				Sources:     cli.EnvVars("CLUSTER_TOKEN"),
			},
			&cli.StringFlag{
				Name:        "host-config-path",
				Usage:       "Path to the host kubeconfig, if empty then virtual-kubelet will use incluster config",
				Destination: &cfg.HostConfigPath,
				Sources:     cli.EnvVars("HOST_KUBECONFIG"),
			},
			&cli.StringFlag{
				Name:        "virtual-config-path",
				Usage:       "Path to the k3k cluster kubeconfig, if empty then virtual-kubelet will create its own config from k3k cluster",
				Destination: &cfg.VirtualConfigPath,
				Sources:     cli.EnvVars("CLUSTER_NAME"),
			},
			&cli.IntFlag{
				Name:        "kubelet-port",
				Usage:       "kubelet API port number",
				Destination: &cfg.KubeletPort,
				Sources:     cli.EnvVars("SERVER_PORT"),
			},
			&cli.IntFlag{
				Name:        "webhook-port",
				Usage:       "Webhook port number",
				Destination: &cfg.WebhookPort,
				Sources:     cli.EnvVars("WEBHOOK_PORT"),
			},
			&cli.StringFlag{
				Name:        "service-name",
				Usage:       "The service name deployed by the k3k controller",
				Destination: &cfg.ServiceName,
				Sources:     cli.EnvVars("SERVICE_NAME"),
			},
			&cli.StringFlag{
				Name:        "agent-hostname",
				Usage:       "Agent Hostname used for TLS SAN for the kubelet server",
				Destination: &cfg.AgentHostname,
				Sources:     cli.EnvVars("AGENT_HOSTNAME"),
			},
			&cli.StringFlag{
				Name:        "server-ip",
				Usage:       "Server IP used for registering the virtual kubelet to the cluster",
				Destination: &cfg.ServerIP,
				Sources:     cli.EnvVars("SERVER_IP"),
			},
			&cli.StringFlag{
				Name:        "version",
				Usage:       "Version of kubernetes server",
				Destination: &cfg.Version,
				Sources:     cli.EnvVars("VERSION"),
			},
			&cli.StringFlag{
				Name:        "config",
				Usage:       "Path to k3k-kubelet config file",
				Destination: &configFile,
				Sources:     cli.EnvVars("CONFIG_FILE"),
				Value:       "/etc/rancher/k3k/config.yaml",
			},
			&cli.BoolFlag{
				Name:        "debug",
				Usage:       "Enable debug logging",
				Destination: &debug,
				Sources:     cli.EnvVars("DEBUG"),
			},
			&cli.BoolFlag{
				Name:        "mirror-host-nodes",
				Usage:       "Mirror real node objects from host cluster",
				Destination: &cfg.MirrorHostNodes,
				Sources:     cli.EnvVars("MIRROR_HOST_NODES"),
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			logger = log.New(debug)
			ctrlruntimelog.SetLogger(zapr.NewLogger(logger.Desugar().WithOptions(zap.AddCallerSkip(1))))

			return ctx, nil
		},
		Action: run,
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	if err := cfg.parse(configFile); err != nil {
		logger.Fatalw("failed to parse config file", "path", configFile, zap.Error(err))
	}

	if err := cfg.validate(); err != nil {
		logger.Fatalw("failed to validate config", zap.Error(err))
	}

	k, err := newKubelet(ctx, &cfg, logger)
	if err != nil {
		logger.Fatalw("failed to create new virtual kubelet instance", zap.Error(err))
	}

	if err := k.registerNode(ctx, k.agentIP, cfg); err != nil {
		logger.Fatalw("failed to register new node", zap.Error(err))
	}

	k.start(ctx)

	return nil
}
