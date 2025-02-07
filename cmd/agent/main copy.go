package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	subdomain := flag.String("subdomain", "test", "Subdomain to register")
	targetPort := flag.String("port", "80", "Local port to expose")
	proxyURL := flag.String("proxy", "ws://localhost:8080/tunnel", "Proxy WebSocket URL")
	apiKey := flag.String("apikey", "test123", "API key for authentication")
	flag.Parse()

	// Connect to proxy
	headers := http.Header{}
	headers.Set("X-API-Key", *apiKey)
	headers.Set("X-Subdomain", *subdomain)

	var conn *websocket.Conn
	var err error

	// Retry connection with backoff
	for {
		conn, _, err = websocket.DefaultDialer.Dial(*proxyURL, headers)
		if err == nil {
			break
		}
		log.Println("Connection failed, retrying...")
		time.Sleep(5 * time.Second)
	}
	defer conn.Close()
	log.Printf("Tunnel active: %s → localhost:%s", *subdomain, *targetPort)

	// Forward WebSocket → Local
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				log.Println("Proxy read error:", err)
				return
			}

			// Forward to local service
			localConn, err := net.Dial("tcp", "localhost:"+*targetPort)
			if err != nil {
				log.Println("Local dial error:", err)
				continue
			}
			defer localConn.Close()

			if _, err := localConn.Write(msg); err != nil {
				log.Println("Local write error:", err)
			}
		}
	}()

	// Forward Local → WebSocket
	localListener, err := net.Listen("tcp", ":"+*targetPort)
	if err != nil {
		log.Fatal("Local listen error:", err)
	}
	defer localListener.Close()

	for {
		localConn, err := localListener.Accept()
		if err != nil {
			log.Println("Local accept error:", err)
			continue
		}

		go func(localConn net.Conn) {
			defer localConn.Close()

			// Read from local and send to WebSocket
			buf := make([]byte, 1024)
			for {
				n, err := localConn.Read(buf)
				if err != nil {
					log.Println("Local read error:", err)
					return
				}

				if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
					log.Println("Proxy write error:", err)
					return
				}
			}
		}(localConn)
	}
}
