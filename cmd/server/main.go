package main

import (
	"log"
	"net/http"
	"os"

	"github.com/gorilla/websocket"
	"pizarreada_backend/internal/game"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// serveWs handles WebSocket requests from the client.
func serveWs(hub *game.Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error al actualizar a WebSocket:", err)
		return
	}
	client := &game.Client{Hub: hub, Conn: conn, Send: make(chan []byte, 256)}
	client.Hub.Register <- client

	// Allow the goroutine state collection when the function returns.
	go client.WritePump()
	go client.ReadPump()
	log.Println("Nuevo cliente WebSocket conectado (IP:", r.RemoteAddr, ")")
}

// serveHome handles requests to the root path and serves the HTML file.
func serveHome(w http.ResponseWriter, r *http.Request) {
	// Ensure that it is only served for the exact path "/"
	// to prevent them from intercepting other requests (such as /ws)
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Get the path of the directory where the binary is executed
	/*
		ex, err := os.Executable()
		if err != nil {
			log.Printf("Error al obtener la ruta del ejecutable: %v", err)
			http.Error(w, "Error interno del servidor", http.StatusInternalServerError)
			return
		}
		exPath := filepath.Dir(ex)
		htmlFilePath := filepath.Join(exPath, "pizarreada.html")*/
	htmlFilePath := "../../web/static/pizarreada.html"

	log.Printf("Intentando servir archivo HTML desde: %s", htmlFilePath)

	// Read the HTML file
	htmlContent, err := os.ReadFile(htmlFilePath)
	if err != nil {
		log.Printf("Error al leer el archivo HTML '%s': %v", htmlFilePath, err)
		http.Error(w, "No se pudo encontrar el archivo HTML. Asegúrate de que 'pizarreada.html' está en el mismo directorio que el ejecutable.", http.StatusNotFound)
		return
	}

	// Set the content type and write the HTML in the response
	w.Header().Set("Content-Type", "text/html; charset=utf-f")
	w.Write(htmlContent)
	log.Println("Archivo HTML servido correctamente.")
}

func main() {
	hub := game.NewHub()
	go hub.Run() // Start the hub in a separate goroutine

	// Serve the HTML file in the root path "/"
	http.HandleFunc("/", serveHome)

	// Serve the WS on the root path "/ws"
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	port := "8080"
	log.Printf("Servidor Pizarreada escuchando en http://localhost:%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
