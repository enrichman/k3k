package cmds

import (
	"github.com/urfave/cli/v3"
)

func NewClusterCmd(appCtx *AppContext) *cli.Command {
	return &cli.Command{
		Name:  "cluster",
		Usage: "cluster command",
		Commands: []*cli.Command{
			NewClusterCreateCmd(appCtx),
			NewClusterDeleteCmd(appCtx),
			NewClusterListCmd(appCtx),
		},
	}
}
