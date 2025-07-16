package k3k_test

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func K3kcli(args ...string) (string, string, error) {
	defaultArgs := []string{"--kubeconfig", kubeconfigPath}
	args = append(defaultArgs, args...)
	args = append(args, defaultArgs...)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}

	cmd := exec.CommandContext(context.Background(), "k3kcli", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	return stdout.String(), stderr.String(), err
}

var _ = When("using the k3kcli", Label("e2e"), func() {

	FIt("can get the version", func() {
		stdout, stderr, err := K3kcli("--version")

		Expect(err).To(Not(HaveOccurred()))
		Expect(stdout).To(ContainSubstring("k3kcli version v"))
		Expect(stderr).To(BeEmpty())
	})

	FIt("can list the clusters", func() {
		stdout, stderr, err := K3kcli("cluster", "list")

		fmt.Fprintf(GinkgoWriter, "## STDOUT:\n\n%s\n\n## STDERR:\n\n%s\n\n## ERR:\n\n%v", stdout, stderr, err)

		Expect(err).To(Not(HaveOccurred()))
		Expect(stdout).To(ContainSubstring("k3kcli version v"))
		Expect(stderr).To(BeEmpty())
	})
})
