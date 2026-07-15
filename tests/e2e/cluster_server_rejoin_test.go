package k3k_test

import (
	"context"
	"time"

	"k8s.io/kubernetes/pkg/api/v1/pod"

	corev1 "k8s.io/api/core/v1"
	ctrlruntimeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
	fwk3k "github.com/rancher/k3k/tests/framework/k3k"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// FWhen focuses this spec so it can be run in isolation (`make test-e2e`).
// Remove the leading F before merging so the full suite runs it.
var _ = FWhen("a persistent HA server rejoins after a scale down and up",
	Label(e2eTestLabel), Label(updateTestsLabel), Label(slowTestsLabel), func() {
		var virtualCluster *VirtualCluster

		ctx := context.Background()

		BeforeEach(func() {
			namespace := fwk3k.CreateNamespace(k8s)

			DeferCleanup(func() {
				fwk3k.DeleteNamespaces(k8s, namespace.Name)
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
				WithTimeout(time.Minute * 3).
				WithPolling(time.Second * 5).
				Should(Succeed())

			By("Scaling up the servers from 1 back to 3")
			// The recreated ordinals re-mount their old PVC, whose etcd data
			// references a member that was already removed. k3s then logs
			// "...please restart k3s to rejoin the cluster" and stays degraded;
			// the liveness probe must restart the pod so k3s can rejoin.
			scaleServers(3)

			By("Verifying the liveness probe restarts a recreated server")
			Eventually(func(g Gomega) {
				serverPods := listServerPods(ctx, virtualCluster)
				g.Expect(serverPods).To(HaveLen(3))

				restarted := 0

				for _, serverPod := range serverPods {
					if len(serverPod.Status.ContainerStatuses) > 0 && serverPod.Status.ContainerStatuses[0].RestartCount > 0 {
						restarted++
					}
				}

				g.Expect(restarted).To(BeNumerically(">=", 1))
			}).
				WithTimeout(time.Minute * 5).
				WithPolling(time.Second * 5).
				Should(Succeed())

			By("Verifying all servers recover to Ready")
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
		})
	})
