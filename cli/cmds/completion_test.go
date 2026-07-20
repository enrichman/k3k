package cmds

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
)

func completionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	schemeBuilder := runtime.NewSchemeBuilder(
		clientgoscheme.AddToScheme,
		v1beta1.AddToScheme,
	)
	assert.NoError(t, schemeBuilder.AddToScheme(scheme))

	return scheme
}

func namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func cluster(name, ns string) *v1beta1.Cluster {
	return &v1beta1.Cluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

func Test_completeNamespaces(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(completionTestScheme(t)).
		WithObjects(
			namespace("default"),
			namespace("k3k-foo"),
			namespace("k3k-bar"),
		).
		Build()

	appCtx := &AppContext{Client: fakeClient}

	names, directive := completeNamespaces(appCtx)(&cobra.Command{}, nil, "")

	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	assert.ElementsMatch(t, []string{"default", "k3k-foo", "k3k-bar"}, names)
}

func Test_completeClusterNamespaces(t *testing.T) {
	fakeClient := fake.NewClientBuilder().
		WithScheme(completionTestScheme(t)).
		WithObjects(
			namespace("default"),
			namespace("k3k-foo"),
			namespace("k3k-bar"),
			cluster("foo", "k3k-foo"),
			cluster("bar", "k3k-bar"),
			// second cluster in the same namespace must be deduped
			cluster("bar-2", "k3k-bar"),
		).
		Build()

	appCtx := &AppContext{Client: fakeClient}

	names, directive := completeClusterNamespaces(appCtx)(&cobra.Command{}, nil, "")

	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
	// only namespaces that contain a cluster, deduplicated; "default" is excluded
	assert.ElementsMatch(t, []string{"k3k-foo", "k3k-bar"}, names)
}
