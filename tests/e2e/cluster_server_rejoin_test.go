package k3k_test

import (
	"context"
	"io"
	"time"

	"k8s.io/kubernetes/pkg/api/v1/pod"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fwk3k "github.com/rancher/k3k/tests/framework/k3k"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Verifies the rejoin liveness probe (rancher/k3k#537): the startup script
// (run_k3s) writes the /var/log/k3s-rejoin sentinel when k3s reports it must
// rejoin the cluster, and the liveness probe (test -f that sentinel) restarts
// the container. We drive the probe deterministically by writing the sentinel
// ourselves, then assert (a) the container is restarted and (b) it recovers to
// Ready, which proves run_k3s clears the sentinel on the next start.
var _ = FWhen("the k3s rejoin sentinel is present", Label(e2eTestLabel), Label(slowTestsLabel), Ordered, func() {
	var virtualCluster *VirtualCluster

	ctx := context.Background()

	BeforeAll(func() {
		virtualCluster = NewVirtualCluster() // ephemeral, single server

		DeferCleanup(func() {
			fwk3k.DeleteNamespaces(k8s, virtualCluster.Cluster.Namespace)
		})
	})

	It("is restarted by the liveness probe and recovers", func() {
		serverPods := listServerPods(ctx, virtualCluster)
		Expect(serverPods).To(HaveLen(1))

		serverPod := serverPods[0]
		namespace := serverPod.Namespace

		var restartsBefore int32
		if len(serverPod.Status.ContainerStatuses) > 0 {
			restartsBefore = serverPod.Status.ContainerStatuses[0].RestartCount
		}

		By("Writing the rejoin sentinel into the server container")
		_, err := podExec(ctx, k8s, restcfg, namespace, serverPod.Name,
			[]string{"sh", "-c", "printf 1 > /var/log/k3s-rejoin"}, nil, io.Discard)
		Expect(err).NotTo(HaveOccurred())

		By("The liveness probe restarts the server container")
		Eventually(func(g Gomega) {
			p, err := k8s.CoreV1().Pods(namespace).Get(ctx, serverPod.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(p.Status.ContainerStatuses).NotTo(BeEmpty())
			g.Expect(p.Status.ContainerStatuses[0].RestartCount).To(BeNumerically(">", restartsBefore))
		}).
			WithTimeout(time.Minute).
			WithPolling(time.Second * 3).
			Should(Succeed())

		By("The server recovers to Ready (run_k3s cleared the sentinel on restart)")
		Eventually(func(g Gomega) {
			p, err := k8s.CoreV1().Pods(namespace).Get(ctx, serverPod.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(HaveOccurred())

			_, cond := pod.GetPodCondition(&p.Status, corev1.PodReady)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(BeEquivalentTo(corev1.ConditionTrue))
		}).
			WithTimeout(time.Minute * 3).
			WithPolling(time.Second * 5).
			Should(Succeed())
	})
})
