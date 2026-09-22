package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/rancher/k3k/pkg/apis/k3k.io/v1beta1"
)

func Test_defaultServerAffinity(t *testing.T) {
	tests := map[string]struct {
		mode    v1beta1.ClusterMode
		servers *int32
		want    bool
	}{
		"hcp with several servers is spread": {
			mode:    v1beta1.HCPClusterMode,
			servers: new(int32(3)),
			want:    true,
		},
		// a single server has nothing to be spread away from
		"hcp with one server": {
			mode:    v1beta1.HCPClusterMode,
			servers: new(int32(1)),
			want:    false,
		},
		"hcp without an explicit server count": {
			mode: v1beta1.HCPClusterMode,
			want: false,
		},
		// only hcp workers reach the servers by node IP, so only hcp needs the spread
		"shared mode": {
			mode:    v1beta1.SharedClusterMode,
			servers: new(int32(3)),
			want:    false,
		},
		"virtual mode": {
			mode:    v1beta1.VirtualClusterMode,
			servers: new(int32(3)),
			want:    false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := &v1beta1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "my-ns"},
				Spec:       v1beta1.ClusterSpec{Mode: tt.mode, Servers: tt.servers},
			}

			affinity := defaultServerAffinity(cluster)

			if !tt.want {
				assert.Nil(t, affinity)
				return
			}

			require.NotNil(t, affinity)
			require.NotNil(t, affinity.PodAntiAffinity)

			// required would make the cluster unschedulable on a host with fewer
			// nodes than servers; the shortfall is reported as a condition instead
			assert.Empty(t, affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution)

			terms := affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution
			require.Len(t, terms, 1)

			assert.Equal(t, corev1.LabelHostname, terms[0].PodAffinityTerm.TopologyKey)
			assert.Equal(t,
				map[string]string{"cluster": "my-cluster", "role": "server"},
				terms[0].PodAffinityTerm.LabelSelector.MatchLabels,
			)
		})
	}
}

// Test_podSpec_serverAffinityPrecedence guards the default from overriding an affinity
// the user or a policy asked for.
func Test_podSpec_serverAffinityPrecedence(t *testing.T) {
	specAffinity := &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{}}
	policyAffinity := &corev1.Affinity{PodAffinity: &corev1.PodAffinity{}}

	tests := map[string]struct {
		spec   *corev1.Affinity
		policy *corev1.Affinity
		want   *corev1.Affinity
	}{
		"spec wins over the default": {
			spec: specAffinity,
			want: specAffinity,
		},
		"policy wins over the spec": {
			spec:   specAffinity,
			policy: policyAffinity,
			want:   policyAffinity,
		},
		"policy wins over the default": {
			policy: policyAffinity,
			want:   policyAffinity,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			cluster := &v1beta1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "my-cluster", Namespace: "my-ns"},
				Spec: v1beta1.ClusterSpec{
					Mode:           v1beta1.HCPClusterMode,
					Servers:        new(int32(3)),
					ServerAffinity: tt.spec,
				},
			}

			if tt.policy != nil {
				cluster.Status.Policy = &v1beta1.AppliedPolicy{ServerAffinity: tt.policy}
			}

			s := &Server{cluster: cluster}

			assert.Equal(t, tt.want, s.podSpec(t.Context(), "image", "name", false, "").Affinity)
		})
	}
}
