package k3k_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	"github.com/rancher/k3k/pkg/apis/k3k.io/v1alpha1"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/k3s"
	"go.uber.org/zap"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTests(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tests Suite")
}

var (
	k3sContainer   *k3s.K3sContainer
	hostIP         string
	k8s            *kubernetes.Clientset
	k8sClient      client.Client
	kubeconfigPath string
)

var _ = BeforeSuite(func() {
	var err error
	ctx := context.Background()

	k3sContainer, err = k3s.Run(ctx, "rancher/k3s:v1.32.1-k3s1")
	Expect(err).To(Not(HaveOccurred()))

	hostIP, err = k3sContainer.ContainerIP(ctx)
	Expect(err).To(Not(HaveOccurred()))
	fmt.Fprintln(GinkgoWriter, "K3s containerIP: "+hostIP)

	kubeconfig, err := k3sContainer.GetKubeConfig(context.Background())
	Expect(err).To(Not(HaveOccurred()))

	initKubernetesClient(kubeconfig)
	installK3kChart(kubeconfig)

	tmpFile, err := os.CreateTemp("", "kubeconfig-")
	Expect(err).To(Not(HaveOccurred()))
	defer tmpFile.Close()

	_, err = tmpFile.Write(kubeconfig)
	Expect(err).To(Not(HaveOccurred()))
	kubeconfigPath = tmpFile.Name()

	DeferCleanup(os.Remove, kubeconfigPath)

	patchDeployment()

	time.Sleep(10 * time.Second)
})

func initKubernetesClient(kubeconfig []byte) {
	restcfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	Expect(err).To(Not(HaveOccurred()))

	k8s, err = kubernetes.NewForConfig(restcfg)
	Expect(err).To(Not(HaveOccurred()))

	scheme := buildScheme()
	k8sClient, err = client.New(restcfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	logger, err := zap.NewDevelopment()
	Expect(err).NotTo(HaveOccurred())
	log.SetLogger(zapr.NewLogger(logger))
}

func installK3kChart(kubeconfig []byte) {
	pwd, err := os.Getwd()
	Expect(err).To(Not(HaveOccurred()))

	k3kChart, err := loader.Load(path.Join(pwd, "../charts/k3k"))
	Expect(err).To(Not(HaveOccurred()))

	actionConfig := new(action.Configuration)

	restClientGetter, err := NewRESTClientGetter(kubeconfig)
	Expect(err).To(Not(HaveOccurred()))

	releaseName := "k3k"
	releaseNamespace := "k3k-system"

	err = actionConfig.Init(restClientGetter, releaseNamespace, os.Getenv("HELM_DRIVER"), func(format string, v ...interface{}) {
		fmt.Fprintf(GinkgoWriter, "helm debug: "+format+"\n", v...)
	})
	Expect(err).To(Not(HaveOccurred()))

	iCli := action.NewInstall(actionConfig)
	iCli.ReleaseName = releaseName
	iCli.Namespace = releaseNamespace
	iCli.CreateNamespace = true
	iCli.Timeout = time.Minute
	iCli.Wait = true

	imageMap, _ := k3kChart.Values["image"].(map[string]any)
	maps.Copy(imageMap, map[string]any{
		"repository": "rancher/k3k",
		"tag":        "dev",
		"pullPolicy": "IfNotPresent",
	})

	sharedAgentMap, _ := k3kChart.Values["sharedAgent"].(map[string]any)
	sharedAgentImageMap, _ := sharedAgentMap["image"].(map[string]any)
	maps.Copy(sharedAgentImageMap, map[string]any{
		"repository": "rancher/k3k-kubelet",
		"tag":        "dev",
	})

	err = k3sContainer.LoadImages(context.Background(), "rancher/k3k:dev", "rancher/k3k-kubelet:dev")
	Expect(err).To(Not(HaveOccurred()))

	release, err := iCli.Run(k3kChart, k3kChart.Values)
	Expect(err).To(Not(HaveOccurred()))

	fmt.Fprintf(GinkgoWriter, "Release %s installed in %s namespace\n", release.Name, release.Namespace)
}

var _ = AfterSuite(func() {
	// dump k3s logs
	readCloser, err := k3sContainer.Logs(context.Background())
	Expect(err).To(Not(HaveOccurred()))

	logs, err := io.ReadAll(readCloser)
	Expect(err).To(Not(HaveOccurred()))

	logfile := path.Join(os.TempDir(), "k3s.log")
	err = os.WriteFile(logfile, logs, 0644)
	Expect(err).To(Not(HaveOccurred()))

	fmt.Fprintln(GinkgoWriter, "k3s logs written to: "+logfile)

	collect()

	// dump k3k controller logs
	readCloser, err = k3sContainer.Logs(context.Background())
	Expect(err).To(Not(HaveOccurred()))
	writeLogs("k3s.log", readCloser)

	// dump k3k logs
	writeK3kLogs()

	testcontainers.CleanupContainer(GinkgoTB(), k3sContainer)
})

func buildScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()

	err := corev1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())
	err = v1alpha1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	return scheme
}

func writeK3kLogs() {
	var (
		err     error
		podList v1.PodList
	)

	ctx := context.Background()
	err = k8sClient.List(ctx, &podList, &client.ListOptions{Namespace: "k3k-system"})
	Expect(err).To(Not(HaveOccurred()))

	k3kPod := podList.Items[0]
	req := k8s.CoreV1().Pods(k3kPod.Namespace).GetLogs(k3kPod.Name, &corev1.PodLogOptions{})
	podLogs, err := req.Stream(ctx)
	Expect(err).To(Not(HaveOccurred()))
	writeLogs("k3k.log", podLogs)
}

func writeLogs(filename string, logs io.ReadCloser) {
	defer logs.Close()

	logsStr, err := io.ReadAll(logs)
	Expect(err).To(Not(HaveOccurred()))

	tempfile := path.Join(os.TempDir(), filename)
	err = os.WriteFile(tempfile, []byte(logsStr), 0644)
	Expect(err).To(Not(HaveOccurred()))

	fmt.Fprintln(GinkgoWriter, "logs written to: "+filename)
}

func patchDeployment() {
	// --- Configuration ---
	namespace := "k3k-system"
	deploymentName := "k3k"
	containerName := "k3k"

	patchPayload := fmt.Sprintf(`{
		"spec": {
			"template": {
				"spec": {
					"volumes": [{
						"name": "tmp-covdata",
						"emptyDir": {}
					}],
					"containers": [{
						"name": "%s",
						"volumeMounts": [{
							"name": "tmp-covdata",
							"mountPath": "/tmp/covdata"
						}],
						"env": [{
							"name": "GOCOVERDIR",
							"value": "/tmp/covdata"
						}]
					}]
				}
			}
		}
	}`, containerName)

	fmt.Fprintf(GinkgoWriter, "Patching deployment '%s' in namespace '%s'...\n", deploymentName, namespace)

	result, err := k8s.AppsV1().Deployments(namespace).Patch(
		context.Background(),
		deploymentName,
		types.StrategicMergePatchType,
		[]byte(patchPayload),
		metav1.PatchOptions{},
	)
	Expect(err).ToNot(HaveOccurred())

	fmt.Fprintf(GinkgoWriter, "Successfully patched deployment. New resource version: %s\n", result.ResourceVersion)
}

func collect() {
	podList, err := k8s.CoreV1().Pods("k3k-system").List(context.Background(), metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=k3k"})
	Expect(err).ToNot(HaveOccurred())
	Expect(len(podList.Items)).To(Equal(1))

	k3kPod := podList.Items[0]
	k3kIP := k3kPod.Status.PodIP

	kexec := exec.Command(
		"kubectl",
		"run",
		"my-wget",
		"--kubeconfig", kubeconfigPath,
		"--namespace", "k3k-system",
		"--image", "busybox",
		"--",
		"wget", "-O-", "http://"+k3kIP+":8088/collect",
	)

	output, err := kexec.CombinedOutput()
	fmt.Fprintln(GinkgoWriter, string(output))
	Expect(err).ToNot(HaveOccurred())

	// wait for the coverage files to be collected
	time.Sleep(5 * time.Second)

	// download the coverage files
	cp := exec.Command(
		"kubectl",
		"cp",
		"--kubeconfig", kubeconfigPath,
		"--namespace", "k3k-system",
		k3kPod.Name+":/tmp/covdata", "covdata",
	)

	output, err = cp.CombinedOutput()
	fmt.Fprintln(GinkgoWriter, string(output))
	Expect(err).ToNot(HaveOccurred())

	covdata := exec.Command("go", "tool", "covdata", "textfmt", "-i=covdata", "-o", "covdata/cover.out")
	output, err = covdata.CombinedOutput()
	fmt.Fprintln(GinkgoWriter, string(output))
	Expect(err).ToNot(HaveOccurred())
}
