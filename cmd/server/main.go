package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var (
	tunnels   = make(map[string]*url.URL) // Maps subdomains to local target URLs
	tunnelsMu sync.RWMutex                // Ensures thread safety

	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true }, // Allow all origins for dev
	}
)

// RegistrationRequest represents the expected JSON request body.
type RegistrationRequest struct {
	Subdomain  string `json:"subdomain"`
	TargetPort string `json:"target_port"`
	APIKey     string `json:"api_key"`
}

func main() {
	// Default tunnel (for testing)
	tunnels["test"], _ = url.Parse("http://localhost:80")

	r := mux.NewRouter()

	// Endpoints
	r.HandleFunc("/register", handleRegister).Methods("POST")
	r.HandleFunc("/tunnel", handleTunnel).Methods("GET")
	r.PathPrefix("/").HandlerFunc(handleHTTP)

	certFile := "test.exposelocal.dev.pem"
	keyFile := "test.exposelocal.dev-key.pem"

	// WebSocket server
	go func() {
		log.Println("Starting WebSocket server on https://exposelocal.dev:8081")
		if err := http.ListenAndServeTLS(":8081", certFile, keyFile, r); err != nil {
			log.Fatal("WebSocket server error:", err)
		}
	}()

	// HTTP reverse proxy
	log.Println("Starting HTTP server on https://exposelocal.dev:8080")
	if err := http.ListenAndServeTLS(":8080", certFile, keyFile, r); err != nil {
		log.Fatal("HTTP server error:", err)
	}
}

// ✅ **Handles WebSocket Connections (Improved)**
func handleTunnel(w http.ResponseWriter, r *http.Request) {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "test123" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket upgrade failed:", err)
		return
	}
	defer conn.Close()

	subdomain := r.Header.Get("X-Subdomain")
	tunnelsMu.RLock()
	target, exists := tunnels[subdomain]
	tunnelsMu.RUnlock()

	if !exists {
		log.Printf("No tunnel found for subdomain: %s", subdomain)
		http.Error(w, "Tunnel not registered", http.StatusNotFound)
		return
	}

	localConn, err := net.Dial("tcp", target.Host)
	if err != nil {
		log.Printf("Failed to connect to %s: %v", target.Host, err)
		http.Error(w, "Target service unavailable", http.StatusBadGateway)
		return
	}
	defer localConn.Close()

	// ✅ **Detect WebSocket Disconnects**
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// ✅ **WebSocket → Local**
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Println("WebSocket read error:", err)
				return
			}
			if _, err := localConn.Write(msg); err != nil {
				log.Println("Local write error:", err)
				return
			}
		}
	}()

	// ✅ **Local → WebSocket**
	buf := make([]byte, 1024)
	for {
		n, err := localConn.Read(buf)
		if err != nil {
			log.Println("Local read error:", err)
			return
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
			log.Println("WebSocket write error:", err)
			return
		}
	}
}

// ✅ **Reverse Proxy (Fixed Subdomain Extraction)**
func handleHTTP(w http.ResponseWriter, r *http.Request) {
	host := strings.Split(r.Host, ".")[0] // Extract subdomain
	tunnelsMu.RLock()
	target, exists := tunnels[host]
	tunnelsMu.RUnlock()

	if !exists {
		http.Error(w, "Tunnel not found", http.StatusNotFound)
		log.Printf("No tunnel found for subdomain: %s", host)
		return
	}

	// ✅ **Create and use a reverse proxy**
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ServeHTTP(w, r)
}

// ✅ **Handles Subdomain Registration (Fixed Mutex & Logs)**
func handleRegister(w http.ResponseWriter, r *http.Request) {
	var req RegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Println("Invalid registration request:", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Validate API key
	if req.APIKey != "test123" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Validate subdomain format
	if !isValidSubdomain(req.Subdomain) {
		http.Error(w, "Invalid subdomain", http.StatusBadRequest)
		return
	}

	// Check for existing subdomain
	tunnelsMu.Lock()
	if _, exists := tunnels[req.Subdomain]; exists {
		tunnelsMu.Unlock()
		http.Error(w, "Subdomain already registered", http.StatusConflict)
		return
	}

	// Register new tunnel
	targetURL, _ := url.Parse("http://localhost:" + req.TargetPort)
	tunnels[req.Subdomain] = targetURL
	tunnelsMu.Unlock()

	log.Printf("Subdomain registered: %s -> %s", req.Subdomain, targetURL.String())
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "Registered Successfully"})
}

// ✅ **Improved Subdomain Validation**
func isValidSubdomain(subdomain string) bool {
	return len(subdomain) > 0 && strings.IndexFunc(subdomain, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-')
	}) == -1
}
