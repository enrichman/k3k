package cmds

import (
	"github.com/urfave/cli/v3"
)

func NewPolicyCmd(appCtx *AppContext) *cli.Command {
	return &cli.Command{
		Name:  "policy",
		Usage: "policy command",
		Commands: []*cli.Command{
			NewPolicyCreateCmd(appCtx),
			NewPolicyDeleteCmd(appCtx),
			NewPolicyListCmd(appCtx),
		},
	}
}
