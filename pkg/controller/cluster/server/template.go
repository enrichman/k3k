package server

var singleServerTemplate string = `
if [ -d "{{.ETCD_DIR}}" ]; then
	# if directory exists then it means its not an initial run

  	# We use the POD_IP from the Downward API as requested
	NODE_NAME=$(hostname)
	SECRET_NAME="${NODE_NAME}.node-password.k3s"
	
	echo "--- 🏥 PHASE 1: ETCD CLUSTER RESET ---"
	# We run this in the foreground. It will rewrite the member list and EXIT automatically.
	# We suppress logs to avoid clutter, as we expect it to 'fail' (exit).
	/bin/k3s server --cluster-reset --node-ip=$POD_IP

	echo "✅ Etcd reset complete (process exited)."

	echo "--- 🏥 PHASE 2: API RESCUE MODE (PATCHING) ---"
	# Now we start the API server in background purely to edit the database.
	# We use --disable-agent to prevent the Flannel/Network panic.
	/bin/k3s server --node-ip=$POD_IP --disable-agent --disable-scheduler --disable-cloud-controller --config {{.INIT_CONFIG}} &

	PID=$!

	# Wait for the API to come online
	echo "⏳ Waiting for API to be ready..."
	until kubectl get nodes > /dev/null 2>&1; do 
		sleep 2
		echo "..."
	done

	echo "--- 🔧 PHASE 3: APPLYING FIXES ---"

	# 1. FIX CREDENTIALS
	echo "🔑 Deleting stale password secret..."
	kubectl delete secret -n kube-system $SECRET_NAME --ignore-not-found --wait=true

	# 2. PATCH NODE ANNOTATIONS
	echo "💉 Patching Node IP records to $POD_IP..."
	kubectl patch node $NODE_NAME --type=merge -p "{\"metadata\":{\"annotations\":{
		\"alpha.kubernetes.io/provided-node-ip\": \"$POD_IP\",
		\"k3s.io/internal-ip\": \"$POD_IP\",
		\"flannel.alpha.coreos.com/public-ip\": \"$POD_IP\",
		\"etcd.k3s.cattle.io/node-address\": \"$POD_IP\"
	}}}"

	# C. REFRESH SYSTEM PODS (Fixes connection refused / stale endpoints)
	echo "♻️  Refreshing system workloads..."
	# We use --force to kill them instantly so the new controller picks them up immediately on boot
	kubectl delete pod -n kube-system --wait=false --ignore-not-found -l k8s-app=metrics-server
	kubectl delete pod -n kube-system --wait=false --ignore-not-found -l k8s-app=kube-dns
	kubectl delete pod -n kube-system --wait=false --ignore-not-found -l k8s-app=local-path-provisioner
	
	# Optional: Refresh Traefik & LoadBalancers if networking is stuck
	kubectl delete pod -n kube-system --wait=false --ignore-not-found -l app.kubernetes.io/name=traefik
	kubectl delete pod -n kube-system --wait=false --ignore-not-found -l app.kubernetes.io/name=svclb-traefik

	echo "🛑 Stopping rescue server..."
	kill $PID
	wait $PID

	/bin/k3s server --node-ip=$POD_IP --config  {{.INIT_CONFIG}} {{.EXTRA_ARGS}} 2>&1 | tee /var/log/k3s.log
fi

rm -f /var/lib/rancher/k3s/server/db/reset-flag
/bin/k3s server --node-ip=$(POD_IP) --config {{.INIT_CONFIG}} {{.EXTRA_ARGS}} 2>&1 | tee /var/log/k3s.log
`

var HAServerTemplate string = `

# --- 1. SETUP VARIABLES ---
NODE_NAME=$(hostname)
SECRET_NAME="${NODE_NAME}.node-password.k3s"
ORDINAL=${NODE_NAME##*-}

echo "--- 🤖 HA SMART BOOT: Node $ORDINAL ($POD_IP) ---"

# --- 2. FOLLOWER LOGIC (Pod 1, 2, etc.) ---
if [ "$ORDINAL" -ne "0" ]; then
	echo "👷 I am a FOLLOWER. Preparing to join..."

	# SAFETY: Wipe local data.
	# If our IP changed, our internal etcd data is invalid anyway.
	# It is safer to wipe and re-replicate from Pod-0 than to try and fix it.
	echo "🧹 Wiping local etcd data to ensure clean join..."
	rm -rf /var/lib/rancher/k3s/server/db/etcd

	echo "🚀 Joining Cluster..."
	/bin/k3s server --node-ip=$(POD_IP) --config {{.SERVER_CONFIG}} {{.EXTRA_ARGS}} 2>&1 | tee /var/log/k3s.log 
fi

# --- 3. LEADER LOGIC (Pod 0) ---
echo "👑 I am the SEED (Pod 0). Checking state..."


echo "--- 🏥 PHASE 2: API RESCUE MODE (PATCHING) ---"

# Now we start the API server in background purely to edit the database.
# We use --disable-agent to prevent the Flannel/Network panic.
/bin/k3s server --node-ip=$POD_IP --disable-agent --disable-scheduler --disable-cloud-controller --config {{.INIT_CONFIG}} &

PID=$!

echo "⏳ Waiting for API (Rescue Mode)..."

# We give it 30 seconds. If it hangs (etcd broken), we kill it and RESET.
TIMEOUT=30
while [ $TIMEOUT -gt 0 ]; do
if kubectl get nodes > /dev/null 2>&1; then
	API_READY=true
	break
fi
sleep 1
TIMEOUT=$((TIMEOUT-1))
done

if [ "$API_READY" = "true" ]; then
	echo "✅ API is healthy. Patching node..."

	# 1. FIX CREDENTIALS
	echo "🔑 Deleting stale password secret..."
	kubectl delete secret -n kube-system $SECRET_NAME --ignore-not-found --wait=true

	# 2. PATCH NODE ANNOTATIONS
	echo "💉 Patching Node IP records to $POD_IP..."
	kubectl patch node $NODE_NAME --type=merge -p "{\"metadata\":{\"annotations\":{
		\"alpha.kubernetes.io/provided-node-ip\": \"$POD_IP\",
		\"k3s.io/internal-ip\": \"$POD_IP\",
		\"flannel.alpha.coreos.com/public-ip\": \"$POD_IP\",
		\"etcd.k3s.cattle.io/node-address\": \"$POD_IP\"
	}}}"

	kill $PID
	wait $PID
else
	echo "⚠️ API failed to start (Etcd Broken?). Triggering CLUSTER RESET..."
	kill $PID
	wait $PID

	# The Nuclear Option: Rewrite etcd identity to this new IP
	/bin/k3s server --cluster-reset --node-ip=$POD_IP $EXTRA_FLAGS
	echo "✅ Reset complete."
fi

echo "🚀 Starting Seed Node..."
/bin/k3s server --node-ip=$(POD_IP) --config {{.INIT_CONFIG}} {{.EXTRA_ARGS}} 2>&1 | tee /var/log/k3s.log
`
