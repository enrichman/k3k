package cmds

import (
	"context"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
)

var completeClusterMode = cobra.FixedCompletions(
	[]string{
		string(v1beta1.SharedClusterMode),
		string(v1beta1.VirtualClusterMode),
	},
	cobra.ShellCompDirectiveNoFileComp,
)

var completePersistenceMode = cobra.FixedCompletions(
	[]string{
		string(v1beta1.DynamicPersistenceMode),
		string(v1beta1.EphemeralPersistenceMode),
	},
	cobra.ShellCompDirectiveNoFileComp,
)

// completionClient builds a Kubernetes client for use inside completion
// functions. Cobra does not run PersistentPreRunE during shell completion, so
// appCtx.Client is usually nil here and we have to build the client ourselves.
func completionClient(appCtx *AppContext) (client.Client, error) {
	if appCtx.Client != nil {
		return appCtx.Client, nil
	}

	restConfig, err := loadRESTConfig(appCtx.Kubeconfig)
	if err != nil {
		return nil, err
	}

	return buildClient(restConfig)
}

// completeNamespaces completes with every namespace in the host cluster.
func completeNamespaces(appCtx *AppContext) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cl, err := completionClient(appCtx)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		var namespaces corev1.NamespaceList
		if err := cl.List(context.Background(), &namespaces); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		// Exclude already selected namespaces
		selected := sets.New[string]()
		if vals, err := cmd.Flags().GetStringSlice("namespace"); err == nil {
			selected.Insert(vals...)
		}

		names := []string{}

		for _, ns := range namespaces.Items {
			if !selected.Has(ns.Name) {
				names = append(names, ns.Name)
			}
		}

		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeClusterNamespaces completes with the unique namespaces that contain at
// least one k3k Cluster, which is the relevant set when acting on an existing
// cluster (delete, list, update, kubeconfig).
func completeClusterNamespaces(appCtx *AppContext) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		cl, err := completionClient(appCtx)
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		var clusters v1beta1.ClusterList
		if err := cl.List(context.Background(), &clusters); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}

		namespaces := sets.New[string]()
		for _, cluster := range clusters.Items {
			namespaces.Insert(cluster.Namespace)
		}

		return sets.List(namespaces), cobra.ShellCompDirectiveNoFileComp
	}
}

// mustRegisterFlagCompletion registers a completion function for a flag and
// aborts if the flag does not exist. This only fails on programmer error, so
// there is no reason to bubble it up to the caller.
func mustRegisterFlagCompletion(cmd *cobra.Command, flagName string, f cobra.CompletionFunc) {
	if err := cmd.RegisterFlagCompletionFunc(flagName, f); err != nil {
		logrus.Fatal(err)
	}
}

// disableFileCompletion walks the command tree and turns off cobra's default
// filename completion for both positional arguments and flag values, leaving
// anything that is explicitly configured (enum completers, MarkFlagFilename,
// MarkFlagDirname, ValidArgs, bool flags) untouched.
func disableFileCompletion(cmd *cobra.Command) {
	if cmd.ValidArgsFunction == nil && len(cmd.ValidArgs) == 0 {
		cmd.ValidArgsFunction = cobra.NoFileCompletions
	}

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" {
			return
		}

		if _, ok := cmd.GetFlagCompletionFunc(f.Name); ok {
			return
		}

		if _, ok := f.Annotations[cobra.BashCompFilenameExt]; ok {
			return
		}

		if _, ok := f.Annotations[cobra.BashCompSubdirsInDir]; ok {
			return
		}

		_ = cmd.RegisterFlagCompletionFunc(f.Name, cobra.NoFileCompletions)
	})

	for _, sub := range cmd.Commands() {
		disableFileCompletion(sub)
	}
}
