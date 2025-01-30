package k3k_test

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = When("hydrophone", Label("hydrophone"), func() {

	var namespace string

	BeforeEach(func() {
		createdNS := &corev1.Namespace{ObjectMeta: v1.ObjectMeta{GenerateName: "ns-"}}
		createdNS, err := k8s.CoreV1().Namespaces().Create(context.Background(), createdNS, v1.CreateOptions{})
		Expect(err).To(Not(HaveOccurred()))
		namespace = createdNS.Name
	})

	It("will be created in shared mode", func() {
		ctx := context.Background()
		containerIP, err := k3sContainer.ContainerIP(ctx)
		Expect(err).To(Not(HaveOccurred()))

		fmt.Fprintln(GinkgoWriter, "K3s containerIP: "+containerIP)

		cluster := v1alpha1.Cluster{
			ObjectMeta: v1.ObjectMeta{
				Name:      "mycluster",
				Namespace: namespace,
			},
			Spec: v1alpha1.ClusterSpec{
				TLSSANs: []string{containerIP},
				Expose: &v1alpha1.ExposeConfig{
					NodePort: &v1alpha1.NodePortConfig{
						Enabled: true,
					},
				},
			},
		}

		kubeconfigPath, _ := CreateCluster(containerIP, cluster)

		ctx, _ = context.WithTimeout(ctx, time.Minute*15)
		args := []string{"--focus", "'Simple pod should contain last line of the log'", "--kubeconfig", kubeconfigPath}
		cmd := exec.CommandContext(ctx, "hydrophone", args...)

		fmt.Fprintf(GinkgoWriter, "Running hydrophone tests... [args=%s]\n", strings.Join(args, " "))

		out, err := cmd.CombinedOutput()
		fmt.Fprintln(GinkgoWriter, string(out))
		Expect(err).To(Not(HaveOccurred()))
	})
})
