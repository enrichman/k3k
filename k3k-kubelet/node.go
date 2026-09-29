package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"

	"github.com/virtual-kubelet/virtual-kubelet/node/nodeutil"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	"k8s.io/client-go/util/retry"

	"github.com/rancher/k3k/pkg/controller"
	"github.com/rancher/k3k/pkg/controller/cluster/server"
	"github.com/rancher/k3k/pkg/k3s"
)

func (k *kubelet) registerNode(agentIP, podIP string, cfg config) error {
	k3sClient := newK3sClient(cfg, k.token, agentIP, podIP)

	clientCA, err := loadClientCA(k3sClient)
	if err != nil {
		return fmt.Errorf("unable to get client ca: %w", err)
	}

	tlsConfig, err := loadTLSConfig(k3sClient, clientCA)
	if err != nil {
		return fmt.Errorf("unable to get tls config: %w", err)
	}

	auth, err := webhookAuth(k, clientCA)
	if err != nil {
		return fmt.Errorf("unable to setup kubelet auth: %w", err)
	}

	mux := http.NewServeMux()

	node, err := nodeutil.NewNode(
		k.name,
		k.newProviderFunc(cfg),
		nodeutil.WithClient(k.virtClient),
		nodeutil.AttachProviderRoutes(mux),
		nodeOpt(nodeutil.WithAuth(auth, mux), tlsConfig, cfg.KubeletPort),
		func(c *nodeutil.NodeConfig) error {
			c.EventRecorder = k.virtEventRecorder
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("unable to start kubelet: %w", err)
	}

	k.node = node

	return nil
}

func nodeOpt(handler http.Handler, tlsConfig *tls.Config, port int) nodeutil.NodeOpt {
	return func(c *nodeutil.NodeConfig) error {
		c.Handler = handler
		c.TLSConfig = tlsConfig

		c.HTTPListenAddr = fmt.Sprintf(":%d", port)

		c.NodeSpec.Labels["kubernetes.io/role"] = "worker"
		c.NodeSpec.Labels["node-role.kubernetes.io/worker"] = "true"

		c.SkipDownwardAPIResolution = true

		return nil
	}
}

// webhookAuth authenticates the kubelet API requests with client certificates signed by the
// virtual cluster client CA or with bearer tokens (TokenReview), and authorizes them against
// the virtual cluster API server (SubjectAccessReview), like a real kubelet in webhook mode.
func webhookAuth(k *kubelet, clientCA []byte) (nodeutil.Auth, error) {
	caProvider, err := dynamiccertificates.NewStaticCAContent("client-ca", clientCA)
	if err != nil {
		return nil, err
	}

	return nodeutil.WebhookAuth(k.virtClient, k.name, func(c *nodeutil.WebhookAuthConfig) error {
		c.AuthnConfig.ClientCertificateCAContentProvider = caProvider
		return nil
	})
}

func newK3sClient(cfg config, token, agentIP, podIP string) *k3s.Client {
	serviceName := fmt.Sprintf("%s.%s", server.ServiceName(cfg.ClusterName), cfg.ClusterNamespace)

	return k3s.New(k3s.ClientConfig{
		ServerIP: serviceName,
		Token:    token,
		AgentIP:  agentIP,
		PodIP:    podIP,
		NodeName: controller.SafeConcatName(cfg.ClusterName, "server-0"),
	})
}

// loadClientCA requests the client CA from the k3s server, used to verify the client
// certificates presented to the kubelet API (i.e. by the kube-apiserver)
func loadClientCA(client *k3s.Client) ([]byte, error) {
	var clientCA []byte

	if err := retry.OnError(controller.Backoff, func(err error) bool {
		return errors.Is(err, k3s.ErrServerNotReady)
	}, func() error {
		var err error

		clientCA, err = client.GetClientCA()

		return err
	}); err != nil {
		return nil, fmt.Errorf("unable to request client ca: %w", err)
	}

	return clientCA, nil
}

// loadTLSConfig function will request kubelet serving crt from k3s server and will use it to
// register a new node to the server, note that we use serving cert to allow adding IPSans to
// the certificate request
func loadTLSConfig(client *k3s.Client, clientCA []byte) (*tls.Config, error) {
	var tlsCrt *tls.Certificate

	if err := retry.OnError(controller.Backoff, func(err error) bool {
		return err == k3s.ErrServerNotReady
	}, func() error {
		var err error

		tlsCrt, err = client.GetServingKubeletCrt()

		return err
	}); err != nil {
		return nil, fmt.Errorf("unable to request serving kubelet certificate: %w", err)
	}

	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(clientCA) {
		return nil, errors.New("unable to parse client ca")
	}

	// client certificates are optional since callers like metrics-server authenticate with bearer tokens,
	// but when presented they must be signed by the client CA
	return &tls.Config{
		Certificates: []tls.Certificate{*tlsCrt},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    clientCAs,
		MinVersion:   tls.VersionTLS12,
	}, nil
}
