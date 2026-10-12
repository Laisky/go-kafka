package kafka

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"github.com/IBM/sarama"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

func tlsTestMaterial(t *testing.T) (tls.Certificate, tls.Certificate, *x509.CertPool) {
	t.Helper()
	makeKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	rootKey := makeKey()
	now := time.Now()
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "synthetic CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	makeCert := func(serial int64, client bool) tls.Certificate {
		key := makeKey()
		cert := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "synthetic peer"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
		if client {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		} else {
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			cert.DNSNames = []string{"localhost"}
			cert.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, rootKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	return makeCert(2, false), makeCert(3, true), pool
}
func validWrapperConfig() *KafkaCliCfg {
	return &KafkaCliCfg{Brokers: []string{"127.0.0.1:9092"}, Topics: []string{"synthetic"}, Groupid: "security-group", KMsgPool: &sync.Pool{New: func() interface{} { return &KafkaMsg{} }}}
}
func TestTransportConfigurationValidation(t *testing.T) {
	cfg := validWrapperConfig()
	effective, err := validatedKafkaConfig(cfg)
	if err != nil || !effective.Net.TLS.Enable || effective.Net.TLS.Config.InsecureSkipVerify || effective.Net.TLS.Config.MinVersion < tls.VersionTLS12 {
		t.Fatalf("secure default missing: %v", err)
	}
	supplied := sarama.NewConfig()
	supplied.Net.TLS.Config = &tls.Config{MinVersion: tls.VersionTLS12}
	supplied.Net.SASL.Enable = true
	supplied.Net.SASL.User = "synthetic-user"
	supplied.Net.SASL.Password = "synthetic-only"
	cfg.ClusterConfig = supplied
	effective, err = validatedKafkaConfig(cfg)
	if err != nil || !effective.Net.SASL.Enable || effective.Net.SASL.User != supplied.Net.SASL.User || !effective.Net.TLS.Enable {
		t.Fatal("SASL over verified TLS was not propagated")
	}
	if supplied.Net.TLS.Enable || effective.Net.TLS.Config == supplied.Net.TLS.Config {
		t.Fatal("caller configuration was mutated or TLS pointer shared")
	}
	cfg.AllowPlaintextForTrustedTunnel = true
	effective, err = validatedKafkaConfig(cfg)
	if err != nil || effective.Net.TLS.Enable {
		t.Fatal("explicit trusted-tunnel opt-in unavailable")
	}
	cfg.AllowPlaintextForTrustedTunnel = false
	for _, unsafe := range []*tls.Config{{InsecureSkipVerify: true}, {MinVersion: tls.VersionTLS10}, {MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS12}} {
		supplied.Net.TLS.Config = unsafe
		if _, err := validatedKafkaConfig(cfg); err == nil {
			t.Fatal("unsafe TLS accepted")
		}
	}
	if _, err := validatedKafkaConfig(nil); err == nil {
		t.Fatal("nil configuration accepted")
	}
	cfg.KMsgPool = nil
	if _, err := validatedKafkaConfig(cfg); err == nil {
		t.Fatal("missing pool accepted")
	}
}
func TestTransportTLSChainHostnameAndMutualAuth(t *testing.T) {
	serverCert, clientCert, roots := tlsTestMaterial(t)
	for _, tc := range []struct {
		name     string
		trust    bool
		hostname string
		mutual   bool
		client   bool
		want     bool
	}{
		{"trusted", true, "", false, false, true},
		{"unknown CA", false, "", false, false, false},
		{"wrong hostname", true, "wrong.invalid", false, false, false},
		{"mutual TLS", true, "", true, true, true},
		{"missing client certificate", true, "", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverCfg := &tls.Config{Certificates: []tls.Certificate{serverCert}, MinVersion: tls.VersionTLS12}
			if tc.mutual {
				serverCfg.ClientAuth = tls.RequireAndVerifyClientCert
				serverCfg.ClientCAs = roots
			}
			listener, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverDone := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverDone <- err
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				err = conn.(*tls.Conn).Handshake()
				if err == nil {
					_, err = conn.Write([]byte{1})
				}
				serverDone <- err
			}()
			cfg := validWrapperConfig()
			custom := sarama.NewConfig()
			custom.Net.TLS.Config = &tls.Config{ServerName: tc.hostname, MinVersion: tls.VersionTLS12}
			if tc.trust {
				custom.Net.TLS.Config.RootCAs = roots
			}
			if tc.client {
				custom.Net.TLS.Config.Certificates = []tls.Certificate{clientCert}
			}
			cfg.ClusterConfig = custom
			effective, err := validatedKafkaConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", listener.Addr().String(), effective.Net.TLS.Config)
			if err == nil {
				conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				var b [1]byte
				_, err = conn.Read(b[:])
				conn.Close()
			}
			if tc.want != (err == nil) {
				t.Fatalf("secure transport accepted=%v want=%v err=%v", err == nil, tc.want, err)
			}
			listener.Close()
			select {
			case <-serverDone:
			case <-time.After(3 * time.Second):
				t.Fatal("owned TLS server did not finish")
			}
		})
	}
}
func TestTransportInvalidPublicConfigurationFailsBeforeDial(t *testing.T) {
	cfg := validWrapperConfig()
	cfg.ClusterConfig = sarama.NewConfig()
	cfg.ClusterConfig.Net.TLS.Config = &tls.Config{InsecureSkipVerify: true}
	if _, err := NewKafkaCliWithGroupID(context.Background(), cfg); err == nil {
		t.Fatal("unsafe public configuration was accepted")
	}
}
