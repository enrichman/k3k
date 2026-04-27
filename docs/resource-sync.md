# Resource Sync Configuration

K3k supports syncing resources between virtual clusters and the host cluster to enable workload execution and infrastructure sharing. This guide explains how resource sync works, which resources can be synced, and how to configure sync behavior for your use case.

**Quick Summary:** K3k implements bidirectional sync. Most resources (Services, ConfigMaps, Secrets, PVCs, Ingresses, PriorityClasses) sync from virtual cluster to host to enable workload execution. StorageClasses sync from host to virtual to share infrastructure resources.

**Important:** The default enabled syncs (Services, ConfigMaps, Secrets, PVCs) are required for most workloads to function correctly. Disabling these should only be done if you know what you're doing and understand the impact on your applications.

For complete API reference, see the [SyncConfig API documentation](./crds/crds.md#syncconfig).

## Understanding Sync Directions

K3k uses a bidirectional sync architecture where resources flow in different directions based on their purpose:

```
┌─────────────────┐         Virtual → Host         ┌──────────────┐
│ Virtual Cluster │ ──────────────────────────────> │ Host Cluster │
│                 │   Services, ConfigMaps,         │              │
│                 │   Secrets, PVCs, Ingresses,     │              │
│                 │   PriorityClasses               │              │
│                 │                                 │              │
│                 │ <────────────────────────────── │              │
│                 │         Host → Virtual          │              │
└─────────────────┘       StorageClasses            └──────────────┘
```

### Virtual to Host Sync (Workload Pattern)

**Direction:** Virtual Cluster → Host Cluster

**Resources:** Services, ConfigMaps, Secrets, PersistentVolumeClaims, Ingresses, PriorityClasses

**Purpose:** Workloads created in the virtual cluster need to be scheduled on the host cluster. These resources must be synced to the host so that pods running on host nodes can access them.

**Implementation Details:**
- Runs in k3k-kubelet (the Virtual Kubelet provider inside the virtual cluster)
- Resources get translated names to prevent collisions: `k3k-{clusterNamespace}-{clusterName}-{resourceNamespace}-{resourceName}`
- Synced resources are tracked with annotations (`k3k.io/name`, `k3k.io/namespace`) and labels (`k3k.io/clusterName`)
- Synced resources are owned by the Cluster CR for automatic cleanup on deletion

**Example:**
```yaml
# Virtual cluster service
apiVersion: v1
kind: Service
metadata:
  name: web
  namespace: default

# Results in synced host service
apiVersion: v1
kind: Service
metadata:
  name: k3k-prod-my-cluster-default-web
  namespace: prod  # cluster's namespace
  annotations:
    k3k.io/name: web
    k3k.io/namespace: default
  labels:
    k3k.io/clusterName: my-cluster
```

### Host to Virtual Sync (Infrastructure Pattern)

**Direction:** Host Cluster → Virtual Cluster

**Resources:** StorageClasses

**Purpose:** Virtual clusters consume infrastructure resources provided by the host. StorageClasses represent available storage backends and need to be visible in the virtual cluster so PVCs can reference them.

**Implementation Details:**
- Runs in the main k3k controller on the host
- Host StorageClasses use the `k3k.io/sync-enabled` label to control sync:
  - Label not present: StorageClass IS synced (default behavior)
  - Label = `"true"`: StorageClass IS synced
  - Label = `"false"`: StorageClass is NOT synced
- Synced resources are labeled with `k3k.io/sync-source: host` in the virtual cluster
- No name translation - StorageClasses keep their original names

**Example:**
```yaml
# Host StorageClass
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast-ssd
  labels:
    k3k.io/sync-enabled: "true"
provisioner: ebs.csi.aws.com

# Results in virtual cluster StorageClass (when sync enabled)
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast-ssd  # Same name
  labels:
    k3k.io/sync-enabled: "true"
    k3k.io/sync-source: host
provisioner: ebs.csi.aws.com
```

## Supported Resource Types

| Resource | Direction | Default | Configuration Level | Selector Support | Special Options |
|----------|-----------|---------|---------------------|------------------|-----------------|
| Services | Virtual → Host | Enabled | Cluster, Policy | Yes | - |
| ConfigMaps | Virtual → Host | Enabled | Cluster, Policy | Yes | - |
| Secrets | Virtual → Host | Enabled | Cluster, Policy | Yes | - |
| PersistentVolumeClaims | Virtual → Host | Enabled | Cluster, Policy | Yes | - |
| Ingresses | Virtual → Host | Disabled | Cluster, Policy | Yes | `disableTLSSecretTranslation` |
| PriorityClasses | Virtual → Host | Disabled | Cluster, Policy | Yes | - |
| StorageClasses | Host → Virtual | Disabled | Cluster, Policy | Yes | Host label opt-in |

### Services

**What:** Network endpoints for accessing workloads running in the virtual cluster.

**Why enabled by default:** Services are essential for workload networking. When pods from the virtual cluster run on host nodes, the corresponding services must exist on the host for network routing to work correctly. Disabling this will break networking for your workloads.

**Use cases:**
- ClusterIP services for internal communication
- LoadBalancer services for external access
- Headless services for StatefulSets

### ConfigMaps

**What:** Configuration data consumed by pods.

**Why enabled by default:** Pods reference ConfigMaps for configuration. Since pods run on the host cluster, the ConfigMaps they reference must be available there. Disable only if your applications don't use any ConfigMaps.

**Use cases:**
- Application configuration files
- Environment-specific settings
- Feature flags

**Example - Selective sync with label selector:**
```yaml
spec:
  sync:
    configMaps:
      enabled: true
      selector:
        app: my-application
```

### Secrets

**What:** Sensitive data like credentials, tokens, and certificates.

**Why enabled by default:** Pods reference Secrets for sensitive data. Since pods run on the host cluster, the Secrets they reference must be available there. Disable only if your applications don't use any Secrets.

**Security note:** Synced secrets remain within the host namespace boundary. They are not accessible across virtual clusters.

**Important:** System secrets (like service-account-token secrets) are NOT synced.

**Use cases:**
- Database passwords
- API keys
- TLS certificates for workloads

**Example - Selective sync with label selector:**
```yaml
spec:
  sync:
    secrets:
      enabled: true
      selector:
        sync-enabled: "true"  # Only sync explicitly labeled secrets
```

### PersistentVolumeClaims

**What:** Storage requests for persistent data.

**Why enabled by default:** Pods need persistent storage for stateful workloads. PVCs are synced to the host where the actual PersistentVolume binding occurs. Disable only if your applications don't use any PersistentVolumeClaims.

**How it works:** 
1. PVC is created in virtual cluster
2. PVC is synced to host cluster
3. Host provisions a PersistentVolume for the PVC
4. A pseudo PV is created in the virtual cluster for scheduling purposes

**Use cases:**
- Database storage
- Application file storage
- StatefulSet volumes

### Ingresses

**What:** HTTP/HTTPS routing rules for external access.

**Why disabled by default:** Not all virtual clusters need external access. Ingresses are cluster-wide routing rules that should be explicitly enabled.

**TLS Secret Translation:** When you create an Ingress in your virtual cluster, any TLS secrets it references are synced from the virtual cluster to the host with translated names (just like other resources). This is the default behavior (`disableTLSSecretTranslation: false`).

If you want to use a shared TLS secret that already exists on the host cluster (for example, a wildcard certificate used across multiple virtual clusters), you can disable TLS secret translation with `disableTLSSecretTranslation: true`. With this option, the Ingress will reference the secret by its original name on the host, allowing you to use centrally-managed certificates.

**Use cases:**
- Production workloads that need external HTTP/HTTPS access
- Custom domain routing
- TLS termination

**Example - Default behavior (secrets synced from virtual cluster):**
```yaml
spec:
  sync:
    ingresses:
      enabled: true
      disableTLSSecretTranslation: false  # Default: sync TLS secrets from virtual cluster
```

**Example - Use shared host TLS secret:**
```yaml
# Host cluster has a wildcard certificate
apiVersion: v1
kind: Secret
metadata:
  name: wildcard-tls-cert
  namespace: default
type: kubernetes.io/tls
data:
  tls.crt: <cert-data>
  tls.key: <key-data>

---
# Virtual cluster Ingress references the shared secret by name
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  sync:
    ingresses:
      enabled: true
      disableTLSSecretTranslation: true  # Use host secret name directly
```

### PriorityClasses

**What:** Pod scheduling priority definitions that affect scheduling order and preemption.

**Why disabled by default:** PriorityClasses are cluster-wide resources that affect scheduling behavior. Allowing virtual clusters to define priorities could lead to priority escalation issues in multi-tenant environments.

**Security consideration:** Carefully evaluate security implications before enabling in multi-tenant scenarios.

**Use cases:**
- Critical production workloads that need guaranteed scheduling
- Multi-tier applications with priority ordering
- Preemption-aware scheduling

**Example:**
```yaml
spec:
  sync:
    priorityClasses:
      enabled: true
```

### StorageClasses

**What:** Storage provisioning templates that define available storage types.

**Why disabled by default:** StorageClasses are infrastructure resources. Enabling sync should be an explicit decision.

**Direction:** Host → Virtual (opposite of other resources)

**Why opposite direction?** StorageClasses represent storage infrastructure provided BY the host cluster. Virtual clusters consume these storage options; they don't provide them.

**Important:** When StorageClass sync is enabled, ALL StorageClasses on the host are synced to your virtual cluster by default. Use the `k3k.io/sync-enabled` label on the host to prevent specific StorageClasses from being synced.

**Two-level filtering:**
1. **Host label (Optional):** `k3k.io/sync-enabled` on the host StorageClass to control sync
   - Label not present: StorageClass WILL be synced
   - Label = `"true"`: StorageClass WILL be synced
   - Label = `"false"`: StorageClass will NOT be synced (use this to block unwanted StorageClasses)
2. **Cluster selector (Optional):** Further filters which StorageClasses to sync based on labels

**Use cases:**
- Multi-tier storage (fast SSD, standard HDD, archive)
- CSI driver-specific storage
- Topology-aware storage (zone-specific, region-specific)

**Example - Sync all StorageClasses:**
```yaml
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
spec:
  sync:
    storageClasses:
      enabled: true  # All host StorageClasses will be synced
```

**Example - Block a StorageClass from syncing:**
```yaml
# On the Host Cluster - prevent this from syncing to virtual clusters
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: admin-only-storage
  labels:
    k3k.io/sync-enabled: "false"  # This StorageClass will NOT be synced
provisioner: custom.csi.driver
```

**Example - Sync only specific StorageClasses using selector:**
```yaml
# Host StorageClass
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast-ssd
  labels:
    tier: premium
provisioner: ebs.csi.aws.com

---
# Cluster configuration - only sync premium tier
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  sync:
    storageClasses:
      enabled: true
      selector:
        tier: premium  # Only sync StorageClasses with tier=premium label
```

**Result in virtual cluster:**
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast-ssd  # Same name as host
  labels:
    k3k.io/sync-source: host  # Indicates sync source
    tier: premium
provisioner: ebs.csi.aws.com
```

## Configuration

### Cluster-Level Configuration

Configure sync for individual clusters via the `spec.sync` field in the Cluster resource.

**Basic example:**
```yaml
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  sync:
    services:
      enabled: true
    configMaps:
      enabled: true
    secrets:
      enabled: true
    persistentVolumeClaims:
      enabled: true
    ingresses:
      enabled: false  # Disabled by default
    priorityClasses:
      enabled: false  # Disabled by default
    storageClasses:
      enabled: false  # Disabled by default
```

### Policy-Level Configuration

Configure sync for all clusters in a namespace via the `spec.sync` field in the VirtualClusterPolicy resource.

**Key behaviors:**
- VirtualClusterPolicy applies to all clusters in the same namespace
- Policy sync configuration overrides cluster-level sync configuration
- Enables centralized control for multi-tenant scenarios

**Example:**
```yaml
apiVersion: k3k.io/v1beta1
kind: VirtualClusterPolicy
metadata:
  name: production-policy
  namespace: prod-clusters
spec:
  sync:
    services:
      enabled: true
    ingresses:
      enabled: true  # Enable for production
      disableTLSSecretTranslation: false
    storageClasses:
      enabled: true
      selector:
        tier: premium  # Only sync premium storage
```

All clusters in the `prod-clusters` namespace will inherit this sync configuration.

### Using Label Selectors

All sync configurations support a `selector` field for filtering resources based on labels. An empty selector matches all resources.

**Example 1 - Only sync ConfigMaps for a specific app:**
```yaml
spec:
  sync:
    configMaps:
      enabled: true
      selector:
        app: my-application
```

**Example 2 - Only sync StorageClasses from a specific CSI driver:**
```yaml
spec:
  sync:
    storageClasses:
      enabled: true
      selector:
        csi-driver: aws-ebs
```

**Example 3 - Combined host label and selector for StorageClasses:**
```yaml
# Host StorageClass
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: fast-ssd
  labels:
    k3k.io/sync-enabled: "true"  # Must have this to be synced
    tier: premium                # Matched by selector
    environment: production
provisioner: ebs.csi.aws.com

---
# Cluster configuration
spec:
  sync:
    storageClasses:
      enabled: true
      selector:
        tier: premium           # Matches the label above
        environment: production
```

Only StorageClasses that match BOTH the host label criteria AND the selector will be synced.

## Common Scenarios

### Development Environment (Full Access)

Enable most sync options for easy development and testing.

```yaml
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: dev-cluster
  namespace: development
spec:
  sync:
    services:
      enabled: true
    configMaps:
      enabled: true
    secrets:
      enabled: true
    persistentVolumeClaims:
      enabled: true
    ingresses:
      enabled: true
    storageClasses:
      enabled: true
    priorityClasses:
      enabled: false  # Still disabled for safety
```

### Multi-Tenant Platform (Policy-Controlled)

Use VirtualClusterPolicy for centralized control and security.

```yaml
apiVersion: k3k.io/v1beta1
kind: VirtualClusterPolicy
metadata:
  name: tenant-policy
  namespace: tenant-clusters
spec:
  sync:
    services:
      enabled: true
    configMaps:
      enabled: true
      selector:
        tenant-allowed: "true"  # Tenants must label their configs
    secrets:
      enabled: true
      selector:
        tenant-allowed: "true"  # Tenants must label their secrets
    persistentVolumeClaims:
      enabled: true
    ingresses:
      enabled: false  # No external access for tenants
    priorityClasses:
      enabled: false  # Prevent priority escalation
    storageClasses:
      enabled: true
      selector:
        tier: standard  # Only standard storage for tenants
```

### Testing Environment (Minimal Sync)

Enable only core sync for ephemeral testing.

```yaml
apiVersion: k3k.io/v1beta1
kind: Cluster
metadata:
  name: test-cluster
  namespace: testing
spec:
  sync:
    services:
      enabled: true
    configMaps:
      enabled: true
    secrets:
      enabled: true
    persistentVolumeClaims:
      enabled: false  # Use ephemeral storage for tests
    ingresses:
      enabled: false
    priorityClasses:
      enabled: false
    storageClasses:
      enabled: false
```

## Verification and Troubleshooting

### Verifying Sync is Working

**Virtual → Host resources:**

```bash
# List synced services from virtual cluster "my-cluster" in namespace "default"
kubectl get services -n default -l k3k.io/clusterName=my-cluster

# Check annotations on a synced resource
kubectl get service k3k-default-my-cluster-default-my-svc -n default -o yaml
# Should have annotations:
#   k3k.io/name: my-svc
#   k3k.io/namespace: default
```

**Host → Virtual resources:**

```bash
# Get kubeconfig for virtual cluster
k3kcli kubeconfig get my-cluster -n default > /tmp/vcluster-kubeconfig

# List StorageClasses in virtual cluster
kubectl --kubeconfig=/tmp/vcluster-kubeconfig get storageclasses

# Check sync source label
kubectl --kubeconfig=/tmp/vcluster-kubeconfig get storageclass fast-ssd -o yaml
# Should have label:
#   k3k.io/sync-source: host
```

### Common Issues

#### StorageClass not appearing in virtual cluster

**Checklist:**
1. **Is sync enabled?** Check `spec.sync.storageClasses.enabled` is `true` in the Cluster
   ```bash
   kubectl get cluster my-cluster -n default -o jsonpath='{.spec.sync.storageClasses.enabled}'
   ```

2. **Check host StorageClass label:** The `k3k.io/sync-enabled` label controls sync
   ```bash
   kubectl get storageclass fast-ssd -o jsonpath='{.metadata.labels.k3k\.io/sync-enabled}'
   ```
   - Not present: Will be synced
   - Value `"true"`: Will be synced
   - Value `"false"`: Will NOT be synced

3. **Check selector matching:** If a selector is configured, verify the StorageClass has matching labels
   ```bash
   # Get cluster selector
   kubectl get cluster my-cluster -n default -o jsonpath='{.spec.sync.storageClasses.selector}'
   
   # Get StorageClass labels
   kubectl get storageclass fast-ssd -o jsonpath='{.metadata.labels}'
   ```

4. **Check controller logs:**
   ```bash
   kubectl logs -n k3k-system deployment/k3k-controller-manager | grep -i storageclass
   ```

#### Service not accessible from host

**Checklist:**
1. **Is sync enabled?** Check `spec.sync.services.enabled` is `true`
   ```bash
   kubectl get cluster my-cluster -n default -o jsonpath='{.spec.sync.services.enabled}'
   ```

2. **Is the service ready in virtual cluster?**
   ```bash
   kubectl --kubeconfig=/tmp/vcluster-kubeconfig get service my-svc -n default
   ```

3. **Does synced service exist on host?**
   ```bash
   kubectl get services -n <cluster-namespace> -l k3k.io/clusterName=<cluster-name>
   ```

4. **Check k3k-kubelet logs** (runs inside virtual cluster):
   ```bash
   kubectl --kubeconfig=/tmp/vcluster-kubeconfig logs -n kube-system deployment/k3k-kubelet
   ```

#### ConfigMap or Secret not syncing

**Checklist:**
1. **Is sync enabled?** Check cluster spec
   ```bash
   kubectl get cluster my-cluster -n default -o jsonpath='{.spec.sync.configMaps.enabled}'
   ```

2. **Check selector matching:** If a selector is configured, verify the resource has matching labels
   ```bash
   kubectl --kubeconfig=/tmp/vcluster-kubeconfig get configmap my-config -o jsonpath='{.metadata.labels}'
   ```

3. **System secrets are not synced:** service-account-token secrets are excluded

4. **Resource not pending deletion:** Resources being deleted won't sync

5. **Check k3k-kubelet logs:**
   ```bash
   kubectl --kubeconfig=/tmp/vcluster-kubeconfig logs -n kube-system deployment/k3k-kubelet | grep -i "configmap\|secret"
   ```

#### Policy sync config not applying

**Checklist:**
1. **Check cluster status for applied policy:**
   ```bash
   kubectl get cluster my-cluster -n default -o jsonpath='{.status.policy}'
   ```

2. **Verify VirtualClusterPolicy is in same namespace:**
   ```bash
   kubectl get virtualclusterpolicy -n <cluster-namespace>
   ```

3. **Check VirtualClusterPolicy status:**
   ```bash
   kubectl get virtualclusterpolicy my-policy -n <namespace> -o yaml
   ```

## Advanced Topics

### Sync Configuration Precedence

When multiple sync configurations exist, they are applied in this order:

1. **VirtualClusterPolicy sync config** (if a policy is applied to the cluster)
2. **Cluster spec sync config**
3. **Default values**

The controller determines which configuration to use:
```go
// From pkg/controller/cluster/cluster.go lines 727-732
appliedSync := cluster.Spec.Sync.DeepCopy()

// If a policy is applied, use its SyncConfig instead
if cluster.Status.Policy != nil && cluster.Status.Policy.Sync != nil {
    appliedSync = cluster.Status.Policy.Sync
}
```

### Resource Cleanup

When sync is disabled for a resource type, existing synced resources are automatically cleaned up:

**Virtual → Host sync cleanup:**
- Synced resources on the host are deleted
- Works via owner references (synced resources are owned by the Cluster CR)
- Deletion happens automatically when the cluster is deleted

**Host → Virtual sync cleanup:**
- Synced resources in the virtual cluster are deleted
- Identified via the `k3k.io/sync-source: host` label
- Cleanup happens when sync is disabled or the cluster is deleted

### Name Translation Details

**For Virtual → Host synced resources:**

Translation pattern: `k3k-{clusterNamespace}-{clusterName}-{resourceNamespace}-{resourceName}`

Example:
- Virtual cluster: `my-cluster` in namespace `prod`
- Virtual service: `web` in namespace `default`
- Synced name: `k3k-prod-my-cluster-default-web`

This prevents naming collisions between multiple virtual clusters sharing the same host namespace.

Original names are tracked in annotations:
```yaml
metadata:
  name: k3k-prod-my-cluster-default-web
  annotations:
    k3k.io/name: web
    k3k.io/namespace: default
  labels:
    k3k.io/clusterName: my-cluster
```

**For Host → Virtual synced resources:**

No name translation occurs. Resources keep their original names since StorageClasses are cluster-scoped and don't risk namespace collisions.

## Related Documentation

- [API Reference - SyncConfig](./crds/crds.md#syncconfig)
- [API Reference - Cluster](./crds/crds.md#cluster)
- [API Reference - VirtualClusterPolicy](./crds/crds.md#virtualclusterpolicy)
- [Virtual Cluster Policy Guide](./virtualclusterpolicy.md)
- [How to Expose Workloads](./howtos/expose-workloads.md)
