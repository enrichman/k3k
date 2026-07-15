package k3k_test

import (
	"context"
	"time"

	"k8s.io/kubernetes/pkg/api/v1/pod"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
	fwk3k "github.com/rancher/k3k/tests/framework/k3k"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// FWhen focuses this spec so it can be run in isolation (`make test-e2e`).
// Remove the leading F before merging so the full suite runs it.
var _ = FWhen("a persistent HA server rejoins after a scale down and up",
	// FlakeAttempts(1) overrides the suite-wide --flake-attempts for this spec:
	// it is slow (creates a fresh HA cluster in BeforeEach), so retrying the
	// whole thing on failure is very expensive and not worth it here.
	FlakeAttempts(1), Label(e2eTestLabel), Label(updateTestsLabel), Label(slowTestsLabel), func() {
		var virtualCluster *VirtualCluster

		ctx := context.Background()

		BeforeEach(func() {
			namespace := fwk3k.CreateNamespace(k8s)

			DeferCleanup(func() {
				fwk3k.DeleteNamespaces(k8s, namespace.Name)
			})

			// On failure, dump each server pod's status and the tail of its k3s
			// output so we can see *why* it was stuck (e.g. the "rejoin the
			// cluster" message). Registered after the namespace cleanup so it
			// runs first (DeferCleanup is LIFO), before the namespace is deleted.
			DeferCleanup(func() {
				if CurrentSpecReport().Failed() {
					dumpServerDiagnostics(ctx, namespace.Name)
				}
			})

			cluster := NewCluster(namespace.Name)

			// HA setup with persistent storage: each server ordinal gets its own
			// retained PVC, which is the precondition for the "rejoin" condition.
			cluster.Spec.Servers = new(int32(3))
			cluster.Spec.Persistence = v1beta1.PersistenceConfig{
				Type: v1beta1.DynamicPersistenceMode,
			}

			CreateCluster(cluster) // waits until all 3 servers are Ready

			client, restConfig := NewVirtualK8sClientAndConfig(cluster)
			virtualCluster = &VirtualCluster{
				Cluster:    cluster,
				RestConfig: restConfig,
				Client:     client,
			}

			Expect(listServerPods(ctx, virtualCluster)).To(HaveLen(3))
		})

		It("is restarted by the liveness probe and recovers", func() {
			scaleServers := func(n int32) {
				var c v1beta1.Cluster
				Expect(k8sClient.Get(ctx, ctrlruntimeclient.ObjectKeyFromObject(virtualCluster.Cluster), &c)).To(Succeed())
				c.Spec.Servers = new(n)
				Expect(k8sClient.Update(ctx, &c)).To(Succeed())
			}

			By("Scaling down the servers from 3 to 1")
			// Scaling down removes the etcd members for ordinals 1 & 2 (via the
			// etcdpod.k3k.io finalizer), while their PVCs are retained.
			scaleServers(1)

			Eventually(func(g Gomega) {
				g.Expect(listServerPods(ctx, virtualCluster)).To(HaveLen(1))
			}).
				WithTimeout(time.Minute * 2).
				WithPolling(time.Second * 5).
				Should(Succeed())

			By("Scaling up the servers from 1 back to 3")
			// The recreated ordinals re-mount their old PVC, whose etcd data
			// references a member that was already removed. k3s then logs
			// "...please restart k3s to rejoin the cluster" and stays degraded
			// without exiting. Because the StatefulSet is OrderedReady, a stuck
			// server blocks the whole scale-up: only the liveness-probe restart
			// lets k3s rejoin so the StatefulSet can progress to all 3 servers.
			scaleServers(3)

			By("Waiting for all servers to recover to Ready")
			// Recovery is two sequential rejoin+restart cycles (server-1 then
			// server-2), so give it a generous timeout.
			Eventually(func(g Gomega) {
				serverPods := listServerPods(ctx, virtualCluster)
				g.Expect(serverPods).To(HaveLen(3))

				for _, serverPod := range serverPods {
					_, cond := pod.GetPodCondition(&serverPod.Status, corev1.PodReady)
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(BeEquivalentTo(corev1.ConditionTrue))
				}
			}).
				WithTimeout(time.Minute * 5).
				WithPolling(time.Second * 10).
				Should(Succeed())

			By("Verifying the recovery happened via a liveness-probe restart")
			// If k3s exited on its own the container would restart without the
			// probe; since it hangs on the rejoin message, a RestartCount > 0
			// proves the probe is what unblocked the recovery.
			var totalRestarts int32
			for _, serverPod := range listServerPods(ctx, virtualCluster) {
				if len(serverPod.Status.ContainerStatuses) > 0 {
					totalRestarts += serverPod.Status.ContainerStatuses[0].RestartCount
				}
			}

			Expect(totalRestarts).To(BeNumerically(">=", 1))
		})
	})

// dumpServerDiagnostics logs the status and the tail of the k3s output of every
// server pod in the namespace. Used on failure to reveal why a server is stuck.
func dumpServerDiagnostics(ctx context.Context, namespace string) {
	GinkgoWriter.Println("=== server pod diagnostics for namespace " + namespace + " ===")

	serverPods, err := k8s.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "role=server"})
	if err != nil {
		GinkgoWriter.Printf("failed to list server pods: %v\n", err)
		return
	}

	tailLines := int64(40)

	for i := range serverPods.Items {
		serverPod := serverPods.Items[i]

		restarts := int32(-1)
		if len(serverPod.Status.ContainerStatuses) > 0 {
			restarts = serverPod.Status.ContainerStatuses[0].RestartCount
		}

		ready := "unknown"
		if _, cond := pod.GetPodCondition(&serverPod.Status, corev1.PodReady); cond != nil {
			ready = string(cond.Status)
		}

		GinkgoWriter.Printf("pod=%s phase=%s ready=%s restarts=%d\n", serverPod.Name, serverPod.Status.Phase, ready, restarts)

		logs, err := k8s.CoreV1().Pods(namespace).GetLogs(serverPod.Name, &corev1.PodLogOptions{TailLines: &tailLines}).DoRaw(ctx)
		if err != nil {
			GinkgoWriter.Printf("  <logs unavailable: %v>\n", err)
			continue
		}

		GinkgoWriter.Printf("  --- last %d log lines ---\n%s\n", tailLines, string(logs))
	}
}
