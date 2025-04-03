package cmds

import (
	"context"
	"errors"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	k3kcluster "github.com/rancher/k3k/pkg/controller/cluster"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func NewClusterSetDeleteCmd(appCtx *AppContext) *cli.Command {
	return &cli.Command{
		Name:            "delete",
		Usage:           "Delete an existing clusterset",
		UsageText:       "k3kcli clusterset delete [command options] NAME",
		Action:          clusterSetDeleteAction(appCtx),
		Flags:           CommonFlags,
		HideHelpCommand: true,
	}
}

func clusterSetDeleteAction(appCtx *AppContext) cli.ActionFunc {
	return func(clx *cli.Context) error {
		ctx := context.Background()
		client := appCtx.Client

		if clx.NArg() != 1 {
			return cli.ShowSubcommandHelp(clx)
		}

		name := clx.Args().First()
		if name == k3kcluster.ClusterInvalidName {
			return errors.New("invalid cluster name")
		}

		namespace := Namespace(name)

		logrus.Infof("Deleting clusterset [%s] in namespace [%s]", name, namespace)

		clusterSet := &v1alpha1.ClusterSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		}

		if err := client.Delete(ctx, clusterSet); err != nil {
			if apierrors.IsNotFound(err) {
				logrus.Warnf("ClusterSet [%s] not found", name)
			} else {
				return err
			}
		}

		return nil
	}
}
