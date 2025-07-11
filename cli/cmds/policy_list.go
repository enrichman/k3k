package cmds

import (
	"context"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/urfave/cli/v3"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/printers"
)

func NewPolicyListCmd(appCtx *AppContext) *cli.Command {
	return &cli.Command{
		Name:            "list",
		Usage:           "List all the existing policies",
		UsageText:       "k3kcli policy list [command options]",
		Action:          policyList(appCtx),
		Flags:           CommonFlags(appCtx),
		HideHelpCommand: true,
	}
}

func policyList(appCtx *AppContext) cli.ActionFunc {
	return func(ctx context.Context, cmd *cli.Command) error {
		client := appCtx.Client

		if cmd.NArg() > 0 {
			return cli.ShowSubcommandHelp(cmd)
		}

		var policies v1alpha1.VirtualClusterPolicyList
		if err := client.List(ctx, &policies); err != nil {
			return err
		}

		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := client.Get(ctx, types.NamespacedName{Name: "virtualclusterpolicies.k3k.io"}, crd); err != nil {
			return err
		}

		items := toPointerSlice(policies.Items)
		table := createTable(crd, items)

		printer := printers.NewTablePrinter(printers.PrintOptions{})

		return printer.PrintObj(table, cmd.Writer)
	}
}
