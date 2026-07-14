package k3k_test

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/kubernetes/pkg/api/v1/pod"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rancher/k3k/k3k-kubelet/translate"
	fwk3k "github.com/rancher/k3k/tests/framework/k3k"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Context("In a shared cluster", Label(e2eTestLabel), Label(slowTestsLabel), Ordered, func() {
	var (
		virtualCluster *VirtualCluster
		translator     *translate.ToHostTranslator
	)

	BeforeAll(func() {
		virtualCluster = NewVirtualCluster()
		translator = translate.NewHostTranslator(virtualCluster.Cluster)

		DeferCleanup(func() {
			fwk3k.DeleteNamespaces(k8s, virtualCluster.Cluster.Namespace)
		})
	})

	When("restarting the k3k-kubelet", func() {
		var (
			virtualPod *corev1.Pod
			hostPodUID types.UID
		)

		BeforeAll(func() {
			ctx := context.Background()

			p := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "nginx-",
					Namespace:    "default",
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "nginx",
						Image: "nginx",
					}},
				},
			}

			var err error

			virtualPod, err = virtualCluster.Client.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{})
			Expect(err).To(Not(HaveOccurred()))

			By("Waiting for the Pod to be Running in the virtual cluster")

			Eventually(func(g Gomega) {
				vPod, err := virtualCluster.Client.CoreV1().Pods(virtualPod.Namespace).Get(ctx, virtualPod.Name, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(vPod.Status.Phase).To(Equal(corev1.PodRunning))
			}).
				WithPolling(time.Second).
				WithTimeout(time.Minute).
				Should(Succeed())

			By("Waiting for the Pod to be Running in the host cluster")

			Eventually(func(g Gomega) {
				hostPodName := translator.NamespacedName(virtualPod)

				hPod, err := k8s.CoreV1().Pods(hostPodName.Namespace).Get(ctx, hostPodName.Name, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hPod.Status.Phase).To(Equal(corev1.PodRunning))
			}).
				WithPolling(time.Second).
				WithTimeout(time.Minute).
				Should(Succeed())

			By("Updating a label on the Pod, to exercise the tracking-metadata-refresh path before the restart")

			virtualPod, err = virtualCluster.Client.CoreV1().Pods(virtualPod.Namespace).Get(ctx, virtualPod.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			if virtualPod.Labels == nil {
				virtualPod.Labels = map[string]string{}
			}

			virtualPod.Labels["k3k.io/test"] = "updated"

			virtualPod, err = virtualCluster.Client.CoreV1().Pods(virtualPod.Namespace).Update(ctx, virtualPod, metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				hostPodName := translator.NamespacedName(virtualPod)

				hPod, err := k8s.CoreV1().Pods(hostPodName.Namespace).Get(ctx, hostPodName.Name, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hPod.Labels).To(HaveKeyWithValue("k3k.io/test", "updated"))
			}).
				WithPolling(time.Second).
				WithTimeout(time.Minute).
				Should(Succeed())

			By("Recording the host and virtual Pod UIDs before the restart")

			hostPodName := translator.NamespacedName(virtualPod)

			hPod, err := k8s.CoreV1().Pods(hostPodName.Namespace).Get(ctx, hostPodName.Name, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())

			hostPodUID = hPod.UID
			Expect(hostPodUID).NotTo(BeEmpty())
		})

		It("should not delete existing Pods on the host or virtual cluster", func() {
			ctx := context.Background()

			By("Restarting every k3k-kubelet agent Pod (one per host node)")

			oldAgentPods := listAgentPods(ctx, virtualCluster)
			Expect(oldAgentPods).NotTo(BeEmpty())

			oldAgentUIDs := map[types.UID]bool{}
			for _, agentPod := range oldAgentPods {
				oldAgentUIDs[agentPod.UID] = true
			}

			for _, agentPod := range oldAgentPods {
				Expect(k8s.CoreV1().Pods(virtualCluster.Cluster.Namespace).Delete(ctx, agentPod.Name, metav1.DeleteOptions{})).To(Succeed())
			}

			By("Waiting for every k3k-kubelet agent Pod to be replaced and become Ready")

			Eventually(func(g Gomega) {
				newAgentPods := listAgentPods(ctx, virtualCluster)
				g.Expect(newAgentPods).To(HaveLen(len(oldAgentPods)))

				for _, agentPod := range newAgentPods {
					g.Expect(oldAgentUIDs).NotTo(HaveKey(agentPod.UID), "Pod %q was not replaced", agentPod.Name)

					_, cond := pod.GetPodCondition(&agentPod.Status, corev1.PodReady)
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(BeEquivalentTo(metav1.ConditionTrue))
				}
			}).
				WithPolling(time.Second).
				WithTimeout(2 * time.Minute).
				Should(Succeed())

			// The dangling-pod cleanup inside the virtual-kubelet runs asynchronously right after
			// startup, and the new agent Pod can report Ready before that pass has actually run
			// or completed. A single check right after restart can pass simply because it ran
			// too early. Poll repeatedly over a longer window, and compare against the UIDs
			// captured before the restart, so a delete-and-recreate (same name, new UID) is
			// caught even if it happens a bit later and even if something else recreated it.

			By("Checking the Pod is never deleted from the virtual cluster")

			Consistently(func(g Gomega) {
				vPod, err := virtualCluster.Client.CoreV1().Pods(virtualPod.Namespace).Get(ctx, virtualPod.Name, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(vPod.UID).To(Equal(virtualPod.UID))
				g.Expect(vPod.Status.Phase).To(Equal(corev1.PodRunning))
			}).
				WithPolling(5 * time.Second).
				WithTimeout(time.Minute).
				Should(Succeed())

			By("Checking the Pod is never deleted from the host cluster")

			Consistently(func(g Gomega) {
				hostPodName := translator.NamespacedName(virtualPod)

				hPod, err := k8s.CoreV1().Pods(hostPodName.Namespace).Get(ctx, hostPodName.Name, metav1.GetOptions{})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(hPod.UID).To(Equal(hostPodUID))
				g.Expect(hPod.Status.Phase).To(Equal(corev1.PodRunning))
			}).
				WithPolling(5 * time.Second).
				WithTimeout(time.Minute).
				Should(Succeed())
		})
	})
})
