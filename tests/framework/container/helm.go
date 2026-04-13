package container

import (
	"maps"
	"os"
	"path"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/release"

	fwclient "github.com/rancher/k3k/tests/framework/client"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// HelmInstaller provides configuration for Helm chart installation.
type HelmInstaller struct {
	ChartPath       string
	Namespace       string
	ReleaseName     string
	Timeout         time.Duration
	Wait            kube.WaitStrategy
	ControllerImage string
	KubeletImage    string
	KubeconfigPath  string
}

// InstallK3kChart installs the k3k Helm chart with the specified configuration.
// It uses the provided RESTClientGetter for authentication and returns the Helm action configuration.
func (h *HelmInstaller) InstallK3kChart(restClientGetter *fwclient.RESTClientGetter) *action.Configuration {
	GinkgoHelper()

	// Load chart
	k3kChart, err := loader.Load(h.ChartPath)
	Expect(err).To(Not(HaveOccurred()))

	// Initialize Helm action configuration
	helmActionConfig := new(action.Configuration)

	err = helmActionConfig.Init(restClientGetter, h.Namespace, os.Getenv("HELM_DRIVER"))
	Expect(err).To(Not(HaveOccurred()))

	// Create install action
	iCli := action.NewInstall(helmActionConfig)
	iCli.ReleaseName = h.ReleaseName
	iCli.Namespace = h.Namespace
	iCli.CreateNamespace = true
	iCli.Timeout = h.Timeout
	iCli.WaitStrategy = h.Wait

	// Get chart accessor to modify values
	chartAccessor, err := chart.NewAccessor(k3kChart)
	Expect(err).To(Not(HaveOccurred()))

	chartValues := chartAccessor.Values()

	// Configure controller image
	controllerMap, _ := chartValues["controller"].(map[string]any)

	extraEnvArray, _ := controllerMap["extraEnv"].([]map[string]any)
	extraEnvArray = append(extraEnvArray, map[string]any{
		"name":  "DEBUG",
		"value": "true",
	})
	controllerMap["extraEnv"] = extraEnvArray

	imageMap, _ := controllerMap["image"].(map[string]any)
	maps.Copy(imageMap, map[string]any{
		"repository": h.ControllerImage,
		"tag":        "dev",
		"pullPolicy": "IfNotPresent",
	})

	// Configure agent image
	agentMap, _ := chartValues["agent"].(map[string]any)
	sharedAgentMap, _ := agentMap["shared"].(map[string]any)
	sharedAgentImageMap, _ := sharedAgentMap["image"].(map[string]any)
	maps.Copy(sharedAgentImageMap, map[string]any{
		"repository": h.KubeletImage,
		"tag":        "dev",
	})

	// Install chart
	releaseResult, err := iCli.Run(k3kChart, chartValues)
	Expect(err).To(Not(HaveOccurred()))

	releaseAccessor, err := release.NewAccessor(releaseResult)
	Expect(err).To(Not(HaveOccurred()))

	GinkgoWriter.Printf("Helm release '%s' installed in '%s' namespace\n", releaseAccessor.Name(), releaseAccessor.Namespace())

	return helmActionConfig
}

// NewHelmInstaller creates a new HelmInstaller with default values.
func NewHelmInstaller(controllerImage, kubeletImage, kubeconfigPath string) *HelmInstaller {
	pwd, err := os.Getwd()
	Expect(err).To(Not(HaveOccurred()))

	return &HelmInstaller{
		ChartPath:       path.Join(pwd, "../../charts/k3k"),
		Namespace:       "k3k-system",
		ReleaseName:     "k3k",
		Timeout:         time.Minute,
		Wait:            kube.StatusWatcherStrategy,
		ControllerImage: controllerImage,
		KubeletImage:    kubeletImage,
		KubeconfigPath:  kubeconfigPath,
	}
}
