package main

import (
	"github.com/rancher/k3k/cli/cmds"
	"github.com/sirupsen/logrus"
)

func main() {
	app := cmds.NewApp()
	if err := app.Execute(); err != nil {
		logrus.Fatal(err)
	}
}
