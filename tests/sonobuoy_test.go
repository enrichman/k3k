package k3k_test

import (
	"context"
	"fmt"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = When("sonobuoy", Label("sonobuoy"), func() {

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

		cmd := exec.Command("sonobuoy", "run", "--wait", "--mode", "quick", "--kubeconfig", kubeconfigPath)
		fmt.Println("Running")

		fmt.Fprintln(GinkgoWriter, "Running sonobuoy tests...")
		Expect(cmd.Run()).To(Not(HaveOccurred()))
	})
})
