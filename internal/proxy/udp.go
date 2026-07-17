package proxy

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"
)

// Session represents a UDP proxy session for a specific client.
type Session struct {
	clientAddr *net.UDPAddr
	serverConn *net.UDPConn
	lastActive time.Time
	closed     bool
	mu         sync.Mutex
}

// Proxy is a UDP reverse proxy that monitors traffic.
type Proxy struct {
	listenAddrStr string
	targetAddrStr string
	onTraffic     func()
	sessions      map[string]*Session
	sessionsMu    sync.RWMutex
	listener      *net.UDPConn
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewProxy creates a new UDP proxy instance.
func NewProxy(listenAddr, targetAddr string, onTraffic func()) *Proxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &Proxy{
		listenAddrStr: listenAddr,
		targetAddrStr: targetAddr,
		onTraffic:     onTraffic,
		sessions:      make(map[string]*Session),
		ctx:           ctx,
		cancel:        cancel,
	}
}

// Start runs the UDP proxy.
func (p *Proxy) Start() error {
	addr, err := net.ResolveUDPAddr("udp", p.listenAddrStr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	p.listener = conn

	log.Printf("[Proxy] Listening on UDP %s, forwarding to %s", p.listenAddrStr, p.targetAddrStr)

	p.wg.Add(2)
	go p.readLoop()
	go p.cleanupLoop()

	return nil
}

// Stop stops the proxy.
func (p *Proxy) Stop() {
	p.cancel()
	if p.listener != nil {
		p.listener.Close()
	}

	p.sessionsMu.Lock()
	for _, s := range p.sessions {
		s.mu.Lock()
		if !s.closed {
			s.serverConn.Close()
			s.closed = true
		}
		s.mu.Unlock()
	}
	p.sessions = make(map[string]*Session)
	p.sessionsMu.Unlock()

	p.wg.Wait()
	log.Printf("[Proxy] Stopped")
}

func (p *Proxy) readLoop() {
	defer p.wg.Done()
	buf := make([]byte, 2048)

	for {
		select {
		case <-p.ctx.Done():
			return
		default:
			n, clientAddr, err := p.listener.ReadFromUDP(buf)
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				log.Printf("[Proxy] Error reading from UDP: %v", err)
				continue
			}

			if n > 0 {
				// Trigger traffic callback (wakes up server if needed)
				p.onTraffic()

				p.forwardPacket(clientAddr, buf[:n])
			}
		}
	}
}

func (p *Proxy) forwardPacket(clientAddr *net.UDPAddr, data []byte) {
	clientKey := clientAddr.String()

	p.sessionsMu.RLock()
	s, exists := p.sessions[clientKey]
	p.sessionsMu.RUnlock()

	if !exists {
		p.sessionsMu.Lock()
		// Double check
		s, exists = p.sessions[clientKey]
		if !exists {
			log.Printf("[Proxy] New session from client: %s", clientKey)
			serverAddr, err := net.ResolveUDPAddr("udp", p.targetAddrStr)
			if err != nil {
				log.Printf("[Proxy] Error resolving target address %s: %v", p.targetAddrStr, err)
				p.sessionsMu.Unlock()
				return
			}

			serverConn, err := net.DialUDP("udp", nil, serverAddr)
			if err != nil {
				log.Printf("[Proxy] Error dialing server: %v", err)
				p.sessionsMu.Unlock()
				return
			}

			s = &Session{
				clientAddr: clientAddr,
				serverConn: serverConn,
				lastActive: time.Now(),
			}
			p.sessions[clientKey] = s

			// Start reading responses back from server to client
			p.wg.Add(1)
			go p.responseLoop(s)
		}
		p.sessionsMu.Unlock()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActive = time.Now()
	_, _ = s.serverConn.Write(data)
}

func (p *Proxy) responseLoop(s *Session) {
	defer p.wg.Done()
	buf := make([]byte, 2048)

	for {
		// Set a read timeout to allow checking if context is done or session closed
		_ = s.serverConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, err := s.serverConn.Read(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					return
				}
				select {
				case <-p.ctx.Done():
					return
				default:
					continue
				}
			}
			s.mu.Lock()
			if !s.closed {
				log.Printf("[Proxy] Error reading from server for client %s: %v", s.clientAddr.String(), err)
			}
			s.mu.Unlock()
			return
		}

		if n > 0 {
			s.mu.Lock()
			if !s.closed {
				s.lastActive = time.Now()
				_, _ = p.listener.WriteToUDP(buf[:n], s.clientAddr)
			}
			s.mu.Unlock()
		}
	}
}

func (p *Proxy) cleanupLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.sessionsMu.Lock()
			now := time.Now()
			for key, s := range p.sessions {
				s.mu.Lock()
				// 5 minutes of idle timeout per individual client session
				if now.Sub(s.lastActive) > 5*time.Minute && !s.closed {
					log.Printf("[Proxy] Closing idle session for %s", key)
					s.serverConn.Close()
					s.closed = true
					delete(p.sessions, key)
				}
				s.mu.Unlock()
			}
			p.sessionsMu.Unlock()
		}
	}
}
