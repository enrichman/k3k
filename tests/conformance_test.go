package k3k_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/rancher/k3k/pkg/controller/certs"
	"github.com/rancher/k3k/pkg/controller/kubeconfig"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
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

		cluster := &v1alpha1.Cluster{
			ObjectMeta: v1.ObjectMeta{
				Name:      "mycluster",
				Namespace: namespace,
			},
			Spec: v1alpha1.ClusterSpec{
				ServerArgs: []string{
					"--kube-apiserver-arg", "feature-gates=kube:DynamicResourceAllocation=true",
				},
				TLSSANs: []string{containerIP},
				Expose: &v1alpha1.ExposeConfig{
					NodePort: &v1alpha1.NodePortConfig{},
				},
			},
		}

		By(fmt.Sprintf("Creating virtual cluster %s/%s", cluster.Namespace, cluster.Name))
		CreateCluster(cluster)

		By("Get the kubeconfig for the virtual cluster")

		var config *clientcmdapi.Config

		Eventually(func() error {
			vKubeconfig := kubeconfig.New()
			kubeletAltName := fmt.Sprintf("k3k-%s-kubelet", cluster.Name)
			vKubeconfig.AltNames = certs.AddSANs([]string{hostIP, kubeletAltName})
			config, err = vKubeconfig.Extract(ctx, k8sClient, cluster, hostIP)
			return err
		}).
			WithTimeout(time.Minute * 2).
			WithPolling(time.Second * 5).
			Should(BeNil())

		kubeconfig, err := clientcmd.Write(*config)
		Expect(err).To(Not(HaveOccurred()))

		tempfile := path.Join(os.TempDir(), cluster.Name+"-kubeconfig.yaml")
		err = os.WriteFile(tempfile, []byte(kubeconfig), 0644)
		Expect(err).To(Not(HaveOccurred()))

		ctx, cancel := context.WithTimeout(ctx, time.Minute*15)
		defer cancel()

		args := []string{"--cleanup", "--focus", "Simple pod should contain last line of the log", "-v", "6", "--kubeconfig", tempfile}
		cmd := exec.CommandContext(ctx, "hydrophone", args...)

		fmt.Fprintf(GinkgoWriter, "Running hydrophone tests... [args=%s]\n", strings.Join(args, " "))

		out, err := cmd.CombinedOutput()
		fmt.Fprintln(GinkgoWriter, string(out))
		Expect(err).To(Not(HaveOccurred()))
	})
})
