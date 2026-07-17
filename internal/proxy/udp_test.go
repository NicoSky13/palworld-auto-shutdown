package proxy

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestUDPProxyTrafficAndRouting(t *testing.T) {
	// 1. Setup a Mock UDP Server (representing Palworld)
	mockServerAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to resolve mock server address: %v", err)
	}

	mockServerConn, err := net.ListenUDP("udp", mockServerAddr)
	if err != nil {
		t.Fatalf("failed to listen on mock server: %v", err)
	}
	defer mockServerConn.Close()

	mockServerActualAddr := mockServerConn.LocalAddr().String()

	// Start reading on Mock server and replying
	serverReceived := make(chan string, 1)
	go func() {
		buf := make([]byte, 1024)
		for {
			n, clientAddr, err := mockServerConn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			data := string(buf[:n])
			serverReceived <- data
			// Reply back to proxy
			_, _ = mockServerConn.WriteToUDP([]byte("reply-"+data), clientAddr)
		}
	}()

	// 2. Setup UDP Proxy
	trafficCalled := int32(0)
	// Bind to 127.0.0.1:0 to get an ephemeral free port
	p := NewProxy("127.0.0.1:0", mockServerActualAddr, func() {
		atomic.StoreInt32(&trafficCalled, 1)
	})

	if err := p.Start(); err != nil {
		t.Fatalf("failed to start proxy: %v", err)
	}
	defer p.Stop()

	// Get the proxy's actual listening address
	proxyActualAddr := p.listener.LocalAddr().String()

	// 3. Client UDP connection
	clientConn, err := net.Dial("udp", proxyActualAddr)
	if err != nil {
		t.Fatalf("failed to dial proxy: %v", err)
	}
	defer clientConn.Close()

	// Send packet to proxy
	_, err = clientConn.Write([]byte("ping"))
	if err != nil {
		t.Fatalf("failed to write to proxy: %v", err)
	}

	// Verify mock server received it
	select {
	case data := <-serverReceived:
		if data != "ping" {
			t.Errorf("expected mock server to receive 'ping', got '%s'", data)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for mock server to receive packet")
	}

	// Verify callback was called
	if atomic.LoadInt32(&trafficCalled) != 1 {
		t.Error("expected onTraffic callback to be called")
	}

	// Verify client receives the reply from mock server routed through proxy
	buf := make([]byte, 1024)
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("failed to read reply from proxy: %v", err)
	}

	reply := string(buf[:n])
	if reply != "reply-ping" {
		t.Errorf("expected client to receive 'reply-ping', got '%s'", reply)
	}
}
