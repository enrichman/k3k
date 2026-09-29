package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/virtual-kubelet/virtual-kubelet/node/nodeutil"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"

	"github.com/rancher/k3k/pkg/k3s"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, cn string) *testCA {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return &testCA{
		cert: cert,
		key:  key,
		pem:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// issue returns a PEM encoded certificate and key signed by the CA
func (ca *testCA) issue(t *testing.T, cn string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// newFakeK3sServer returns a k3s client for a fake k3s server serving the kubelet serving certificate and the client CA
func newFakeK3sServer(t *testing.T, servingCA, clientCA *testCA) *k3s.Client {
	t.Helper()

	servingCrt, servingKey := servingCA.issue(t, "k3k-kubelet", x509.ExtKeyUsageServerAuth)

	mux := http.NewServeMux()
	mux.HandleFunc("/v1-k3s/serving-kubelet.crt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(append(servingCrt, servingKey...))
	})
	mux.HandleFunc("/v1-k3s/client-ca.crt", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(clientCA.pem)
	})

	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	return k3s.New(k3s.ClientConfig{ServerIP: u.Host})
}

// newFakeVirtClient returns a client for a fake API server authenticating the "valid-token" bearer token
// as "token-user", and authorizing only the users in allowedUsers to access the nodes resource.
// The returned function lists the SubjectAccessReviews received by the fake API server.
func newFakeVirtClient(t *testing.T, allowedUsers ...string) (kubernetes.Interface, func() []authorizationv1.SubjectAccessReview) {
	t.Helper()

	var (
		mu      sync.Mutex
		reviews []authorizationv1.SubjectAccessReview
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/apis/authentication.k8s.io/v1/tokenreviews", func(w http.ResponseWriter, r *http.Request) {
		var review authenticationv1.TokenReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if review.Spec.Token == "valid-token" {
			review.Status.Authenticated = true
			review.Status.User = authenticationv1.UserInfo{Username: "token-user"}
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	})
	mux.HandleFunc("/apis/authorization.k8s.io/v1/subjectaccessreviews", func(w http.ResponseWriter, r *http.Request) {
		var review authorizationv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		mu.Lock()

		reviews = append(reviews, review)

		mu.Unlock()

		review.Status.Allowed = slices.Contains(allowedUsers, review.Spec.User) &&
			review.Spec.ResourceAttributes != nil &&
			review.Spec.ResourceAttributes.Resource == "nodes"

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)

	return client, func() []authorizationv1.SubjectAccessReview {
		mu.Lock()
		defer mu.Unlock()

		return slices.Clone(reviews)
	}
}

func Test_KubeletAPIAuth(t *testing.T) {
	servingCA := newTestCA(t, "server-ca")
	clientCA := newTestCA(t, "client-ca")
	otherCA := newTestCA(t, "other-ca")

	k3sClient := newFakeK3sServer(t, servingCA, clientCA)

	caPEM, err := loadClientCA(k3sClient)
	require.NoError(t, err)

	tlsConfig, err := loadTLSConfig(k3sClient, caPEM)
	require.NoError(t, err)

	virtClient, _ := newFakeVirtClient(t, "system:apiserver", "token-user")

	auth, err := kubeletAuth(virtClient, "test-node", caPEM)
	require.NoError(t, err)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewUnstartedServer(nodeutil.WithAuth(auth, handler))
	srv.TLS = tlsConfig
	srv.StartTLS()
	t.Cleanup(srv.Close)

	serverCAs := x509.NewCertPool()
	serverCAs.AddCert(servingCA.cert)

	newClient := func(t *testing.T, ca *testCA, cn string) *http.Client {
		t.Helper()

		cfg := &tls.Config{RootCAs: serverCAs, MinVersion: tls.VersionTLS12}

		if ca != nil {
			certPEM, keyPEM := ca.issue(t, cn, x509.ExtKeyUsageClientAuth)
			cert, err := tls.X509KeyPair(certPEM, keyPEM)
			require.NoError(t, err)

			// always present the certificate, even if it's not signed by one of the CAs advertised by the server
			cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				return &cert, nil
			}
		}

		return &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	}

	get := func(t *testing.T, client *http.Client, token string) (*http.Response, error) {
		t.Helper()

		req, err := http.NewRequest(http.MethodGet, srv.URL+"/runningpods/", nil)
		require.NoError(t, err)

		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}

		return client.Do(req)
	}

	tests := []struct {
		name         string
		ca           *testCA
		cn           string
		token        string
		expectStatus int
		expectErr    bool
	}{
		{
			name:         "anonymous request is rejected",
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "authorized client certificate is allowed",
			ca:           clientCA,
			cn:           "system:apiserver",
			expectStatus: http.StatusOK,
		},
		{
			name:         "unauthorized client certificate is forbidden",
			ca:           clientCA,
			cn:           "some-user",
			expectStatus: http.StatusForbidden,
		},
		{
			name:      "client certificate from another CA fails the handshake",
			ca:        otherCA,
			cn:        "system:apiserver",
			expectErr: true,
		},
		{
			name:         "valid bearer token is allowed",
			token:        "valid-token",
			expectStatus: http.StatusOK,
		},
		{
			name:         "invalid bearer token is rejected",
			token:        "invalid-token",
			expectStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := get(t, newClient(t, tt.ca, tt.cn), tt.token)
			if tt.expectErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)

			defer func() {
				_ = resp.Body.Close()
			}()

			assert.Equal(t, tt.expectStatus, resp.StatusCode)
		})
	}
}

// Test_KubeletAuthHandler checks the auth handler on its own, without the TLS listener: client certificates
// not signed by the client CA are rejected even if they get past TLS, and requests are authorized as nodes/proxy
func Test_KubeletAuthHandler(t *testing.T) {
	clientCA := newTestCA(t, "client-ca")
	otherCA := newTestCA(t, "other-ca")

	parseCert := func(t *testing.T, ca *testCA, cn string) *x509.Certificate {
		t.Helper()

		certPEM, _ := ca.issue(t, cn, x509.ExtKeyUsageClientAuth)
		block, _ := pem.Decode(certPEM)

		cert, err := x509.ParseCertificate(block.Bytes)
		require.NoError(t, err)

		return cert
	}

	tests := []struct {
		name         string
		cert         *x509.Certificate
		expectStatus int
	}{
		{
			name:         "certificate from another CA is rejected",
			cert:         parseCert(t, otherCA, "system:apiserver"),
			expectStatus: http.StatusUnauthorized,
		},
		{
			name:         "authorized certificate is allowed as nodes/proxy",
			cert:         parseCert(t, clientCA, "system:apiserver"),
			expectStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			virtClient, reviews := newFakeVirtClient(t, "system:apiserver")

			auth, err := kubeletAuth(virtClient, "test-node", clientCA.pem)
			require.NoError(t, err)

			handler := nodeutil.WithAuth(auth, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/containerLogs/ns/pod/container", nil)
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{tt.cert}}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.expectStatus, rec.Code)

			if tt.expectStatus == http.StatusUnauthorized {
				assert.Empty(t, reviews())
				return
			}

			require.Len(t, reviews(), 1)

			attrs := reviews()[0].Spec.ResourceAttributes
			require.NotNil(t, attrs)
			assert.Equal(t, "nodes", attrs.Resource)
			assert.Equal(t, "proxy", attrs.Subresource)
			assert.Equal(t, "test-node", attrs.Name)
		})
	}
}
