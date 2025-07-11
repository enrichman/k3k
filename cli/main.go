package main

import (
	"context"
	"os"

	"github.com/rancher/k3k/cli/cmds"
	"github.com/sirupsen/logrus"
)

func main() {
	app := cmds.NewApp()
	if err := app.Run(context.Background(), os.Args); err != nil {
		logrus.Fatal(err)
	}
}
