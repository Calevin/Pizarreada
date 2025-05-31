package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// upgrader It is used to upgrade an HTTP connection to a WebSocket connection.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all connections for development.
		// In production, verify the origin.
		return true
	},
}

// Client represents a single connected WebSocket client.
type Client struct {
	conn     *websocket.Conn
	hub      *Hub
	send     chan []byte // Channel to send messages to the client.
	username string
	isReady  bool
	mu       sync.Mutex // To protect access to username and isReady
}

// Hub maintains the active client pool and transmits messages to clients.
type Hub struct {
	clients    map[*Client]bool // Registered clients.
	broadcast  chan []byte      // Incoming messages from clients.
	register   chan *Client     // Channel to register clients.
	unregister chan *Client     // Channel to unregister the client.
	mu         sync.Mutex       // To protect client access.
}

// Message defines the structure of messages exchanged via WebSocket.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// UserPayload it is used for messages related to user identification.
type UserPayload struct {
	Username string `json:"username"`
}

// ChatPayload used for chat messages.
type ChatPayload struct {
	Username string `json:"username"`
	Message  string `json:"message"`
}

// ReadyPayload used for "ready" status messages.
type ReadyPayload struct {
	Username string `json:"username"`
	IsReady  bool   `json:"isReady"`
}

// UserListPayload it is used to send the list of connected users.
type UserListPayload struct {
	Users []UserDetails `json:"users"`
}

// UserDetails contains information about a logged in user.
type UserDetails struct {
	Username string `json:"username"`
	IsReady  bool   `json:"isReady"`
}

// newHub creates a new Hub instance.
func newHub() *Hub {
	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

// run starts processing messages from the hub.
func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			log.Printf("Cliente conectado: %s (Total: %d)", client.username, len(h.clients))
			h.mu.Unlock()
			h.broadcastUserList() // Send the updated list after registration

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Printf("Cliente desconectado: %s (Total: %d)", client.username, len(h.clients))
			}
			h.mu.Unlock()
			if client.username != "" { // Only transmit if the user has logged in
				h.broadcastUserList() // Send the updated list after deregistration
			}

		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.send <- message:
					log.Printf("client.send %s <- message...", client.username)
				default:
					// If the sending channel is blocked, the client may be disconnected.
					close(client.send)
					delete(h.clients, client)
					log.Printf("Cliente eliminado por canal de envío bloqueado: %s", client.username)
				}
			}
			h.mu.Unlock()
		}
	}
}

// broadcastUserList sends the current list of users to all clients.
func (h *Hub) broadcastUserList() {
	h.mu.Lock()
	defer h.mu.Unlock()

	var userList []UserDetails
	for client := range h.clients {
		client.mu.Lock()
		// Only include identified users
		if client.username != "" {
			userList = append(userList, UserDetails{Username: client.username, IsReady: client.isReady})
		}
		client.mu.Unlock()
	}

	payloadBytes, err := json.Marshal(UserListPayload{Users: userList})
	if err != nil {
		log.Printf("Error al codificar UserListPayload: %v", err)
		return
	}

	msg := Message{Type: "userListUpdate", Payload: payloadBytes}
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Error al codificar mensaje userListUpdate: %v", err)
		return
	}

	// Send the list of users to all clients.
	for client := range h.clients {
		select {
		case client.send <- msgBytes:
		default:
			log.Printf("Error al enviar userListUpdate a %s: canal de envío bloqueado.", client.username)
		}
	}
	log.Println("Lista de usuarios transmitida:", string(msgBytes))
}

// checkAllReady checks if all connected users are ready.
func (h *Hub) checkAllReady() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.clients) == 0 {
		return
	}

	allReady := true
	identifiedUserCount := 0
	for client := range h.clients {
		client.mu.Lock()
		// Only consider identified users
		if client.username != "" {
			identifiedUserCount++
			if !client.isReady {
				allReady = false
			}
		}
		client.mu.Unlock()
	}

	// Si solo hay un usuario identificado (Gandalf en modo desarrollo), puede empezar solo.
	if identifiedUserCount == 1 {
		var singleClient *Client
		for c := range h.clients {
			if c.username != "" {
				singleClient = c
				break
			}
		}
		if singleClient != nil && singleClient.username == "Gandalf" && singleClient.isReady {
			log.Println("Gandalf está listo y puede empezar solo.")
			// No se envía "allReady" porque el inicio lo maneja el cliente de Gandalf
			return
		}
	}

	// For >1 players, everyone must be ready
	if allReady && identifiedUserCount > 0 {
		log.Println("Todos los jugadores están listos. Transmitiendo inicio de juego.")
		msg := Message{Type: "allPlayersReady", Payload: json.RawMessage(`{}`)}
		msgBytes, _ := json.Marshal(msg)
		h.broadcast <- msgBytes // Use the hub's broadcast channel
	} else {
		log.Printf("No todos los jugadores están listos. Identificados: %d, Listos: %d de %d clientes", identifiedUserCount, countReadyPlayers(h), len(h.clients))
	}
}

// countReadyPlayers count how many identified players are ready.
func countReadyPlayers(h *Hub) int {
	// The hub's mu.Lock() must already be acquired by the caller (checkAllReady)
	readyCount := 0
	for client := range h.clients {
		client.mu.Lock()
		if client.username != "" && client.isReady {
			readyCount++
		}
		client.mu.Unlock()
	}
	return readyCount
}

// readPump pumps messages from the WebSocket connection to the hub.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
		log.Printf("Conexión cerrada para: %s (readPump)", c.username)
	}()
	// Set message limits?
	// var maxMessageSize int64 = 300
	// c.conn.SetReadLimit(maxMessageSize)
	// Configure pong handler for keep-alive if necessary
	// c.conn.SetReadDeadline(time.Now().Add(pongWait))
	// c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })

	for {
		_, rawMessage, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Error de lectura de WebSocket: %v", err)
			}
			break // Exit the loop if there is an error (e.g. client disconnected)
		}

		var msg Message
		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			log.Printf("Error al decodificar mensaje JSON: %v. Mensaje: %s", err, string(rawMessage))
			continue
		}

		log.Printf("Mensaje recibido: Tipo=%s, Payload=%s", msg.Type, string(msg.Payload))

		switch msg.Type {
		case "userIdentify":
			var payload UserPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar UserPayload: %v", err)
				continue
			}
			c.mu.Lock()
			c.username = payload.Username
			c.isReady = false // By default it is not ready to log in
			log.Printf("Usuario identificado: %s", c.username)
			c.mu.Unlock()
			c.hub.broadcastUserList() // Notify everyone about the new user/updated list

		case "chatMessage":
			log.Printf("chatMessage...")
			var payload ChatPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar ChatPayload: %v", err)
				continue
			}
			// Repackage the message for transmission, ensuring that the type is correct.
			chatMsgBytes, _ := json.Marshal(msg) // Use the original message already parsed
			c.hub.broadcast <- chatMsgBytes

		case "playerReady":
			var payload ReadyPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar ReadyPayload: %v", err)
				continue
			}
			c.mu.Lock()
			// Ensure the client updates its own status
			if c.username == payload.Username {
				c.isReady = payload.IsReady
				log.Printf("Usuario %s cambió estado de listo a: %t", c.username, c.isReady)
			}
			c.mu.Unlock()
			c.hub.broadcastUserList() // Notify everyone about the status change and updated list
			c.hub.checkAllReady()     // Check if everyone is ready

		case "requestStartGame": // Mensaje enviado por Gandalf para iniciar solo
			c.mu.Lock()
			isGandalf := c.username == "Gandalf"
			c.mu.Unlock()
			if isGandalf {
				log.Printf("Gandalf solicitó iniciar el juego.")
				// Enviar mensaje de inicio solo a Gandalf o manejarlo como un inicio global si es el único.
				// Por ahora, la lógica de inicio para Gandalf se maneja más en el cliente
				// pero el servidor puede confirmar o iniciar la secuencia de juego.
				// Para este ejemplo, solo registramos. El cliente de Gandalf ya se encarga.
				// Podríamos enviar un mensaje "gameStartingForGandalf" si fuera necesario.
				// O, si es el único jugador, checkAllReady podría manejarlo.
				c.hub.checkAllReady() // Re-evaluar, Gandalf podría estar listo
			}

		// Aquí se añadirían más tipos de mensajes para la lógica del juego (dibujo, adivinanzas, etc.)
		default:
			log.Printf("Tipo de mensaje desconocido: %s", msg.Type)
		}
	}
}

// writePump pumps messages from the hub to the WebSocket connection.
func (c *Client) writePump() {
	// Configure ticker for pings if necessary
	// ticker := time.NewTicker(pingPeriod)
	defer func() {
		// ticker.Stop()
		c.conn.Close()
		log.Printf("Conexión cerrada para: %s (writePump)", c.username)
	}()
	for {
		select {
		case message, ok := <-c.send:
			// c.conn.SetWriteDeadline(time.Now().Add(writeWait)) // Set writing deadline
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				log.Printf("Error al obtener NextWriter: %v", err)
				return
			}
			_, err = w.Write(message)
			if err != nil {
				log.Printf("Error al escribir: %v", err)
				return
			}

			// Añadir mensajes en cola al writer actual si los hay.
			// n := len(c.send)
			// for i := 0; i < n; i++ {
			// 	w.Write(<-c.send)
			// }

			if err := w.Close(); err != nil {
				log.Printf("Error al cerrar writer: %v", err)
				return
			}
			log.Printf("Mensaje enviado a %s: %s", c.username, string(message))

			// case <-ticker.C: // Periodic ping
			// 	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			// 	if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
			// 		return
			// 	}
		}
	}
}

// serveWs handles WebSocket requests from the client.
func serveWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("Error al actualizar a WebSocket:", err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256), username: "", isReady: false}
	client.hub.register <- client

	// Allow the goroutine state collection when the function returns.
	go client.writePump()
	go client.readPump()

	log.Println("Nuevo cliente WebSocket conectado.")
}

func main() {
	hub := newHub()
	go hub.run() // Start the hub in a separate goroutine

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	port := "8080"
	log.Printf("Servidor WebSocket escuchando en el puerto %s", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
