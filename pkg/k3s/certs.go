package k3s

import (
	"crypto/tls"
	"net/http"
)

// GetServingKubeletCrt returns the serving certificate the k3s server issues for the kubelet.
func (c *Client) GetServingKubeletCrt() (*tls.Certificate, error) {
	endpoint := "/v1-k3s/serving-kubelet.crt"

	tlsCrtData, err := c.do(endpoint, "node", http.MethodGet, nil)
	if err != nil {
		return nil, err
	}

	tlsCrt, err := tls.X509KeyPair(tlsCrtData, tlsCrtData)
	if err != nil {
		return nil, err
	}

	return &tlsCrt, nil
}

// GetClientCA returns the PEM encoded client CA of the k3s server, used to verify
// client certificates such as the one the kube-apiserver presents to the kubelet.
func (c *Client) GetClientCA() ([]byte, error) {
	endpoint := "/v1-k3s/client-ca.crt"

	return c.do(endpoint, "node", http.MethodGet, nil)
}
