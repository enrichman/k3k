package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
	"github.com/rancher/k3k/pkg/controller"
)

func Test_findNonLoopbackSAN(t *testing.T) {
	tests := []struct {
		name string
		sans []string
		want string
	}{
		{
			name: "loopback first, external second",
			sans: []string{"127.0.0.1", "10.0.0.100"},
			want: "10.0.0.100",
		},
		{
			name: "external first",
			sans: []string{"10.0.0.100", "127.0.0.1"},
			want: "10.0.0.100",
		},
		{
			name: "only loopback",
			sans: []string{"127.0.0.1", "::1"},
			want: "",
		},
		{
			name: "localhost hostname filtered",
			sans: []string{"localhost", "example.com"},
			want: "example.com",
		},
		{
			name: "external hostname",
			sans: []string{"hcp.example.com"},
			want: "hcp.example.com",
		},
		{
			name: "empty",
			sans: []string{},
			want: "",
		},
		{
			name: "ipv6 loopback filtered",
			sans: []string{"::1", "2001:db8::1"},
			want: "2001:db8::1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findNonLoopbackSAN(tt.sans)
			assert.Equal(t, tt.want, got)
		})
	}
}

func Test_hcpEndpointAddress(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantIP       string
		wantHostname string
		wantErr      bool
	}{
		{
			name:         "ipv4 literal is passed through",
			input:        "10.144.101.195",
			wantIP:       "10.144.101.195",
			wantHostname: "",
			wantErr:      false,
		},
		{
			name:    "unresolvable hostname errors",
			input:   "definitely-not-a-real-host.invalid",
			wantErr: true,
		},
		{
			name:    "ipv4 loopback literal is rejected",
			input:   "127.0.0.1",
			wantErr: true,
		},
		{
			name:    "ipv6 loopback literal is rejected",
			input:   "::1",
			wantErr: true,
		},
		{
			name:    "ipv4 loopback in range is rejected",
			input:   "127.0.0.100",
			wantErr: true,
		},
		{
			name:         "valid ipv6 literal passes through",
			input:        "2001:db8::1",
			wantIP:       "2001:db8::1",
			wantHostname: "",
			wantErr:      false,
		},
		{
			name:    "localhost hostname filters loopbacks",
			input:   "localhost",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hcpEndpointAddress(context.Background(), tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)

			assert.Equal(t, tt.wantIP, got.IP)
			assert.Equal(t, tt.wantHostname, got.Hostname)
		})
	}
}

// Test_serverNodeIPs covers the collapse behind https://github.com/rancher/k3k/issues/1002:
// servers sharing a host node share a published address, so the returned IPs are fewer
// than the servers they are meant to address.
func Test_serverNodeIPs(t *testing.T) {
	tests := []struct {
		name          string
		podNodes      []string
		nodes         []corev1.Node
		wantIPs       []string
		wantScheduled int
	}{
		{
			name:          "one server per node",
			podNodes:      []string{"node-a", "node-b", "node-c"},
			nodes:         []corev1.Node{testNode("node-a", "10.0.0.1"), testNode("node-b", "10.0.0.2"), testNode("node-c", "10.0.0.3")},
			wantIPs:       []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"},
			wantScheduled: 3,
		},
		{
			name:          "three servers on a single node collapse to one address",
			podNodes:      []string{"node-a", "node-a", "node-a"},
			nodes:         []corev1.Node{testNode("node-a", "10.0.0.1")},
			wantIPs:       []string{"10.0.0.1"},
			wantScheduled: 3,
		},
		{
			name:          "two of three servers share a node",
			podNodes:      []string{"node-a", "node-a", "node-b"},
			nodes:         []corev1.Node{testNode("node-a", "10.0.0.1"), testNode("node-b", "10.0.0.2")},
			wantIPs:       []string{"10.0.0.1", "10.0.0.2"},
			wantScheduled: 3,
		},
		{
			name:          "unscheduled servers are not counted",
			podNodes:      []string{"node-a", "", ""},
			nodes:         []corev1.Node{testNode("node-a", "10.0.0.1")},
			wantIPs:       []string{"10.0.0.1"},
			wantScheduled: 1,
		},
		{
			// a node that has gone away must not drop the endpoints of the others
			name:          "missing node is skipped",
			podNodes:      []string{"node-a", "node-gone"},
			nodes:         []corev1.Node{testNode("node-a", "10.0.0.1")},
			wantIPs:       []string{"10.0.0.1"},
			wantScheduled: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &v1beta1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "my-ns"},
			}

			objs := []client.Object{cluster}
			for i, nodeName := range tt.podNodes {
				objs = append(objs, testServerPod(cluster, i, nodeName))
			}

			for i := range tt.nodes {
				objs = append(objs, &tt.nodes[i])
			}

			reconciler := &Reconciler{Client: newFakeClient(t, objs...)}

			ips, scheduled, err := reconciler.serverNodeIPs(context.Background(), cluster, true)
			require.NoError(t, err)

			assert.Equal(t, tt.wantIPs, ips)
			assert.Equal(t, tt.wantScheduled, scheduled)
		})
	}
}

func Test_hcpEndpoints_unreachableServers(t *testing.T) {
	tests := []struct {
		name      string
		endpoints hcpEndpoints
		want      int
	}{
		{
			name:      "one address per server",
			endpoints: hcpEndpoints{ips: []string{"10.0.0.1", "10.0.0.2"}, servers: 2},
			want:      0,
		},
		{
			name:      "three servers behind one address",
			endpoints: hcpEndpoints{ips: []string{"10.0.0.1"}, servers: 3},
			want:      2,
		},
		{
			// an Ingress or LoadBalancer cannot address an individual server, so there
			// is nothing to compare against and nothing to report
			name:      "single front door",
			endpoints: hcpEndpoints{ips: []string{"10.0.0.1"}},
			want:      0,
		},
		{
			// a node with several internal IPs in the same family
			name:      "more addresses than servers",
			endpoints: hcpEndpoints{ips: []string{"10.0.0.1", "10.0.0.2"}, servers: 1},
			want:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.endpoints.unreachableServers())
		})
	}
}

func Test_setHCPEndpointsCondition(t *testing.T) {
	tests := []struct {
		name       string
		endpoints  hcpEndpoints
		wantStatus metav1.ConditionStatus
		wantReason string
	}{
		{
			name:       "every server reachable",
			endpoints:  hcpEndpoints{ips: []string{"10.0.0.1", "10.0.0.2"}, servers: 2},
			wantStatus: metav1.ConditionTrue,
			wantReason: ReasonEndpointsReconciled,
		},
		{
			name:       "servers sharing a host node",
			endpoints:  hcpEndpoints{ips: []string{"10.0.0.1"}, servers: 3},
			wantStatus: metav1.ConditionFalse,
			wantReason: ReasonServersShareHostNode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &v1beta1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "my-ns"},
			}

			reconciler := &Reconciler{EventRecorder: events.NewFakeRecorder(10)}
			reconciler.setHCPEndpointsCondition(cluster, tt.endpoints)

			condition := meta.FindStatusCondition(cluster.Status.Conditions, ConditionHCPEndpointsReady)
			require.NotNil(t, condition)

			assert.Equal(t, tt.wantStatus, condition.Status)
			assert.Equal(t, tt.wantReason, condition.Reason)

			// the phase is owned by updateStatus and must be left alone: a cluster
			// whose servers share a node is degraded, not broken
			assert.Empty(t, cluster.Status.Phase)
		})
	}
}

// Test_setHCPEndpointsCondition_eventsOnlyOnTransition guards against an event on every
// reconcile while the degradation is standing.
func Test_setHCPEndpointsCondition_eventsOnlyOnTransition(t *testing.T) {
	cluster := &v1beta1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "my-ns"},
	}

	recorder := events.NewFakeRecorder(10)
	reconciler := &Reconciler{EventRecorder: recorder}

	degraded := hcpEndpoints{ips: []string{"10.0.0.1"}, servers: 3}

	reconciler.setHCPEndpointsCondition(cluster, degraded)
	require.Len(t, recorder.Events, 1)

	reconciler.setHCPEndpointsCondition(cluster, degraded)
	assert.Len(t, recorder.Events, 1, "a standing condition must not re-emit its event")

	reconciler.setHCPEndpointsCondition(cluster, hcpEndpoints{ips: []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}, servers: 3})
	assert.Len(t, recorder.Events, 2, "recovering must emit an event")
}

func testServerPod(cluster *v1beta1.Cluster, ordinal int, nodeName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("k3k-%s-server-%d", cluster.Name, ordinal),
			Namespace: cluster.Namespace,
			Labels:    map[string]string{"cluster": cluster.Name, "role": "server"},
		},
		Spec: corev1.PodSpec{NodeName: nodeName},
	}
}

func testNode(name, ip string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeHostName, Address: name},
				{Type: corev1.NodeInternalIP, Address: ip},
			},
		},
	}
}

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()

	scheme := runtime.NewScheme()

	schemeBuilder := runtime.NewSchemeBuilder(
		corev1.AddToScheme,
		v1beta1.AddToScheme,
	)
	require.NoError(t, schemeBuilder.AddToScheme(scheme))

	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

// Compile-time assertion: every reused exported name from the controller
// package below this test file must remain stable. If `controller.K3SImage`
// disappears (refactor), this guards the dependency.
var _ = controller.K3SImage
