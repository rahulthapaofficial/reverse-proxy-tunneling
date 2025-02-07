package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	// Command-line flags
	subdomainFlag := flag.String("subdomain", "test", "Subdomain for the tunnel")
	targetPort := flag.String("port", "80", "Local port to expose (e.g., Apache on 80)")
	proxyURL := flag.String("proxy", "wss://exposelocal.dev:8081/tunnel", "Proxy WebSocket URL")
	apiKey := flag.String("apikey", "test123", "Authentication key")
	flag.Parse()

	// Initial subdomain
	subdomain := *subdomainFlag

	// Register subdomain with proxy
	for {
		registerURL := "https://exposelocal.dev:8080/register"
		registerData := map[string]string{
			"subdomain":   subdomain,
			"target_port": *targetPort,
			"api_key":     *apiKey,
		}

		jsonData, err := json.Marshal(registerData)
		if err != nil {
			log.Fatalf("JSON encoding failed: %v", err)
		}

		log.Printf("Registering subdomain: %s", subdomain)
		resp, err := http.Post(registerURL, "application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			log.Printf("HTTP request failed: %v", err)
			time.Sleep(5 * time.Second) // Retry after 5 seconds
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Printf("Registration response: %d - %s", resp.StatusCode, string(body))

		if resp.StatusCode == http.StatusCreated {
			log.Println("Successfully Registered")
			break // Successfully registered
		}

		if resp.StatusCode == http.StatusConflict {
			subdomain = fmt.Sprintf("%s-%d", *subdomainFlag, rand.Intn(1000))
			log.Printf("Subdomain taken, retrying with: %s", subdomain)
			continue
		}

		log.Fatalf("Registration failed: %s", string(body))
	}

	// Graceful shutdown handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	headers := http.Header{}
	headers.Set("X-API-Key", *apiKey)
	headers.Set("X-Subdomain", subdomain)

	retryDelay := 2 * time.Second
	maxRetryDelay := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			log.Println("Shutting down agent...")
			return
		default:
			log.Printf("Connecting to WebSocket: %s", *proxyURL)
			conn, _, err := websocket.DefaultDialer.Dial(*proxyURL, headers)
			if err != nil {
				log.Printf("WebSocket connection failed: %v. Retrying in %v...", err, retryDelay)
				time.Sleep(retryDelay)
				retryDelay = increaseDelay(retryDelay, maxRetryDelay)
				continue
			}

			log.Printf("Tunnel active: https://%s.exposelocal.dev → localhost:%s", subdomain, *targetPort)
			retryDelay = 2 * time.Second // Reset retry delay

			// Handle the connection
			connectionCtx, cancel := context.WithCancel(ctx)
			go handleConnection(connectionCtx, conn, *targetPort)

			// Wait for connection to drop
			<-connectionCtx.Done()
			cancel()
			conn.Close()
		}
	}
}

func handleConnection(ctx context.Context, conn *websocket.Conn, targetPort string) {
	defer conn.Close()

	log.Printf("Starting local listener on port %s...", targetPort)
	localListener, err := net.Listen("tcp", ":"+targetPort)
	if err != nil {
		log.Fatalf("Local listener error: %v", err)
	}
	defer localListener.Close()

	for {
		select {
		case <-ctx.Done():
			return
		default:
			localConn, err := localListener.Accept()
			if err != nil {
				log.Println("Local accept error:", err)
				continue
			}

			go forwardTraffic(ctx, localConn, conn)
		}
	}
}

func forwardTraffic(ctx context.Context, localConn net.Conn, wsConn *websocket.Conn) {
	defer localConn.Close()

	// Local → WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, err := localConn.Read(buf)
				if err != nil {
					log.Println("Local read error:", err)
					return
				}

				if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					log.Println("WebSocket write error:", err)
					return
				}
			}
		}
	}()

	// WebSocket → Local
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_, msg, err := wsConn.ReadMessage()
			if err != nil {
				log.Println("WebSocket read error:", err)
				return
			}

			if _, err := localConn.Write(msg); err != nil {
				log.Println("Local write error:", err)
				return
			}
		}
	}
}

func increaseDelay(currentDelay, max time.Duration) time.Duration {
	next := currentDelay * 2
	if next > max {
		return max
	}
	return next
}
