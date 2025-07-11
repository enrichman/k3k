package main

import (
	"fmt"
	"os"
	"path"

	"github.com/rancher/k3k/cli/cmds"
	docs "github.com/urfave/cli-docs/v3"
)

func main() {
	// Instantiate the CLI application
	app := cmds.NewApp()

	// Generate the Markdown documentation
	md, err := docs.ToMarkdown(app)
	if err != nil {
		fmt.Println("Error generating documentation:", err)
		os.Exit(1)
	}

	wd, err := os.Getwd()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	outputFile := path.Join(wd, "docs/cli/cli-docs.md")

	err = os.WriteFile(outputFile, []byte(md), 0644)
	if err != nil {
		fmt.Println("Error generating documentation:", err)
		os.Exit(1)
	}

	fmt.Println("Documentation generated at " + outputFile)
}
