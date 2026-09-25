package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yoyo/gotunnel/internal/client"
	"github.com/yoyo/gotunnel/internal/config"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func testCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func startLocalService(t *testing.T, handler func(net.Conn)) (int, func()) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go handler(conn)
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, func() { cancel(); l.Close() }
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}

func TestTLSTunnelConcurrentTrafficReconnectSlowServiceAndShutdown(t *testing.T) {
	echoPort, stopEcho := startLocalService(t, func(conn net.Conn) {
		defer conn.Close()
		io.Copy(conn, conn)
	})
	defer stopEcho()
	slowPort, stopSlow := startLocalService(t, func(conn net.Conn) {
		defer conn.Close()
		time.Sleep(2 * time.Second)
	})
	defer stopSlow()

	controlPort, echoRemote, slowRemote := freePort(t), freePort(t), freePort(t)
	cfg := config.NewManager("")
	cfg.InitDefault()
	if err := cfg.SetServerPort(controlPort); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetPanelPort(0); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetClientPassword("client-secret"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SetAdminPassword("admin-secret"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddTunnelPorts("integration", echoRemote, echoPort); err != nil {
		t.Fatal(err)
	}
	if err := cfg.AddTunnelPorts("integration", slowRemote, slowPort); err != nil {
		t.Fatal(err)
	}

	certPath, keyPath := testCertificate(t)
	srv := NewServer(cfg)
	if err := srv.EnableTLS(certPath, keyPath); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	cli := client.NewClient(fmt.Sprintf("localhost:%d", controlPort), "integration", "client-secret")
	if err := cli.EnableTLS(certPath, "localhost"); err != nil {
		t.Fatal(err)
	}
	if err := cli.SetReconnectInterval(20 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 3*time.Second, func() bool { return srv.GetClientConnection("integration") != nil })
	slow, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", slowRemote))
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	go io.Copy(slow, &endlessReader{})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", echoRemote), time.Second)
			if err != nil {
				t.Errorf("dial %d: %v", i, err)
				return
			}
			defer conn.Close()
			payload := []byte(fmt.Sprintf("message-%d", i))
			if _, err := conn.Write(payload); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, got); err != nil || string(got) != string(payload) {
				t.Errorf("echo %d: got %q, err %v", i, got, err)
			}
		}(i)
	}
	wg.Wait()

	old := srv.GetClientConnection("integration")
	old.Close()
	waitFor(t, 3*time.Second, func() bool {
		current := srv.GetClientConnection("integration")
		return current != nil && current != old
	})

	if err := cli.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
}

type endlessReader struct{}

func (*endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}
