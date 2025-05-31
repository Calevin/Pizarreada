package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// GameState represents the current state of the game.
type GameState struct {
	currentDrawerUsername string
	currentWord           string
	gameInProgress        bool
	playerOrder           []*Client // To determine the next Dibujante
	currentPlayerIndex    int       // Index in playerOrder of the current Dibujante
	mu                    sync.Mutex
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
	gameState  *GameState       // Game state
	gameWords  []string         // Words available for the game
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

// AssignDrawerPayload to send words to the dibujante.
type AssignDrawerPayload struct {
	DrawerUsername string   `json:"drawerUsername"`
	WordsToChoose  []string `json:"wordsToChoose,omitempty"` // Only for the dibujante
}

// WordChosenPayload when the dibujante chooses a word.
type WordChosenPayload struct {
	Username   string `json:"username"` // The dibujante
	ChosenWord string `json:"chosenWord"`
}

// DrawingPhaseStartPayload to notify start of drawing.
type DrawingPhaseStartPayload struct {
	DrawerUsername string `json:"drawerUsername"`
	WordToDraw     string `json:"wordToDraw,omitempty"` // Only for the dibujante
	Duration       int    `json:"duration"`             // Duration in seconds to draw
}

// DrawActionPayload for canvas drawing actions.
type DrawActionPayload struct {
	Action   string  `json:"action"` // "start", "draw", "end", "clear", "controlChange"
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	FromX    float64 `json:"fromX,omitempty"`
	FromY    float64 `json:"fromY,omitempty"`
	ToX      float64 `json:"toX,omitempty"`
	ToY      float64 `json:"toY,omitempty"`
	Color    string  `json:"color,omitempty"`
	Size     float64 `json:"size,omitempty"`
	IsEraser bool    `json:"isEraser,omitempty"`
	Control  string  `json:"control,omitempty"`  // e.g., "color", "brushSize", "eraser"
	Value    string  `json:"value,omitempty"`    // For control changes, e.g. color hex, size string
	Username string  `json:"username,omitempty"` // Username of the dibujante performing the action
}

func newGameState() *GameState {
	return &GameState{
		gameInProgress:     false,
		currentPlayerIndex: -1, // No one is drawing initially
	}
}

// newHub creates a new Hub instance.
func newHub() *Hub {
	rand.Seed(time.Now().UnixNano()) // Initialize random number generator
	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		gameState:  newGameState(),
		gameWords:  []string{"perro", "casa", "pelota", "silla", "dragon", "arbol", "sol", "luna", "estrella", "rio", "montaña", "flor"},
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			log.Printf("Cliente registrado (Total: %d)", len(h.clients))
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			username := client.username // Save before the customer leaves
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				log.Printf("Cliente desconectado: %s (Total: %d)", username, len(h.clients))
			}
			h.mu.Unlock()
			if username != "" {
				h.broadcastUserList()
				// If a player leaves during the game (e.g. end turn, choose new dibujante)
				h.gameState.mu.Lock()
				if h.gameState.gameInProgress && h.gameState.currentDrawerUsername == username {
					log.Printf("El dibujante %s se desconectó. Terminando turno.", username)
					// TODO: logic to move on to the next dibujante
					h.gameState.gameInProgress = false
					h.broadcastGameEnd("El dibujante se desconectó.")
				}
				h.gameState.mu.Unlock()
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

// startGame starts the game.
func (h *Hub) startGame() {
	h.gameState.mu.Lock()
	defer h.gameState.mu.Unlock()

	if h.gameState.gameInProgress {
		log.Println("Intento de iniciar juego, pero ya está en progreso.")
		return
	}

	h.mu.Lock() // Lock hub to securely access h.clients
	var readyPlayers []*Client
	for client := range h.clients {
		client.mu.Lock()
		if client.username != "" && client.isReady {
			readyPlayers = append(readyPlayers, client)
		}
		client.mu.Unlock()
	}
	h.mu.Unlock()

	if len(readyPlayers) == 0 {
		log.Println("No hay jugadores listos para empezar el juego.")
		return
	}

	// Mix up the order of players taking turns
	rand.Shuffle(len(readyPlayers), func(i, j int) {
		readyPlayers[i], readyPlayers[j] = readyPlayers[j], readyPlayers[i]
	})

	h.gameState.playerOrder = readyPlayers
	h.gameState.currentPlayerIndex = -1 // It will be incremented to 0 on assignNextDrawer the first time
	h.gameState.gameInProgress = true
	log.Printf("Juego iniciado. Orden de jugadores: %v", getPlayerUsernames(h.gameState.playerOrder))

	h.assignNextDrawer()
}

// getPlayerUsernames returns a list of usernames for the given players.
func getPlayerUsernames(players []*Client) []string {
	var usernames []string
	for _, p := range players {
		p.mu.Lock()
		usernames = append(usernames, p.username)
		p.mu.Unlock()
	}
	return usernames
}

// assignNextDrawer
func (h *Hub) assignNextDrawer() {
	// gameState.mu should already be locked by the caller (startGame or after a turn)
	if !h.gameState.gameInProgress || len(h.gameState.playerOrder) == 0 {
		log.Println("No se puede asignar dibujante, juego no en progreso o no hay jugadores.")
		h.gameState.gameInProgress = false // Ensure that the game does not continue
		h.broadcastGameEnd("No hay jugadores para continuar.")
		return
	}
	h.gameState.currentPlayerIndex++ // Move to next
	if h.gameState.currentPlayerIndex >= len(h.gameState.playerOrder) {
		log.Println("Todos los jugadores han dibujado en esta ronda. Fin de ronda/juego.")
		h.gameState.gameInProgress = false
		h.broadcastGameEnd("Ronda completada.")
		// TODO: Lógica para nueva ronda o fin de juego total
		return
	}

	drawerClient := h.gameState.playerOrder[h.gameState.currentPlayerIndex]
	drawerClient.mu.Lock()
	h.gameState.currentDrawerUsername = drawerClient.username
	drawerClient.mu.Unlock()
	h.gameState.currentWord = "" // Resetear palabra actual

	log.Printf("Asignando dibujante: %s", h.gameState.currentDrawerUsername)

	// Select 3 random words
	wordsToChoose := h.getRandomWords(3)

	// Send a message to the artist with the words
	drawerPayload := AssignDrawerPayload{
		DrawerUsername: h.gameState.currentDrawerUsername,
		WordsToChoose:  wordsToChoose,
	}
	drawerPayloadBytes, _ := json.Marshal(drawerPayload)
	drawerMsg := Message{Type: "assignDrawerAndWords", Payload: drawerPayloadBytes}
	drawerMsgBytes, _ := json.Marshal(drawerMsg)

	// Send only to the drawing client
	select {
	case drawerClient.send <- drawerMsgBytes:
		log.Printf("Mensaje assignDrawerAndWords enviado a %s", drawerClient.username)
	default:
		log.Printf("Error al enviar assignDrawerAndWords a %s: canal bloqueado", drawerClient.username)
	}

	// Send the message to other players (adivinadores)
	guesserPayload := AssignDrawerPayload{DrawerUsername: h.gameState.currentDrawerUsername} // Without WordsToChoose
	guesserPayloadBytes, _ := json.Marshal(guesserPayload)
	guesserMsg := Message{Type: "assignDrawerAndWords", Payload: guesserPayloadBytes}
	guesserMsgBytes, _ := json.Marshal(guesserMsg)

	h.mu.Lock() // Block hub to iterate on clients
	for client := range h.clients {
		if client != drawerClient {
			select {
			case client.send <- guesserMsgBytes:
			default:
				log.Printf("Error al enviar assignDrawerAndWords (guesser) a %s: canal bloqueado", client.username)
			}
		}
	}
	h.mu.Unlock()
}

func (h *Hub) getRandomWords(count int) []string {
	if len(h.gameWords) == 0 {
		return []string{"default1", "default2", "default3"} // Fallback
	}
	rand.Shuffle(len(h.gameWords), func(i, j int) {
		h.gameWords[i], h.gameWords[j] = h.gameWords[j], h.gameWords[i]
	})

	numToPick := count
	if len(h.gameWords) < count {
		numToPick = len(h.gameWords)
	}
	return h.gameWords[:numToPick]
}

// broadcastGameEnd sends a message to all clients indicating the end of the game.
// reason is a string describing the reason for the game ending.
func (h *Hub) broadcastGameEnd(reason string) {
	log.Println("Transmitiendo fin de juego:", reason)
	payload := map[string]string{"reason": reason}
	payloadBytes, _ := json.Marshal(payload)
	msg := Message{Type: "gameEnded", Payload: payloadBytes}
	msgBytes, _ := json.Marshal(msg)
	h.broadcast <- msgBytes
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
				log.Printf("Error de lectura de WebSocket para %s: %v", c.username, err)
			}
			break // Exit the loop if there is an error (e.g. client disconnected)
		}

		var msg Message
		if err := json.Unmarshal(rawMessage, &msg); err != nil {
			log.Printf("Error al decodificar mensaje JSON: %v. Mensaje: %s", err, string(rawMessage))
			continue
		}
		log.Printf("Mensaje recibido de %s: Tipo=%s", c.username, msg.Type)

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
			c.mu.Unlock()
			log.Printf("Usuario identificado: %s", c.username)
			c.hub.broadcastUserList() // Notify everyone about the new user/updated list

		case "chatMessage":
			log.Printf("chatMessage...")
			var payload ChatPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar ChatPayload: %v", err)
				continue
			}
			// Add the server username to ensure it is correct
			payload.Username = c.username // Overwrite in case the client sends another one
			chatPayloadBytes, _ := json.Marshal(payload)
			// Repackage the message for transmission, ensuring that the type is correct.
			finalMsg := Message{Type: "chatMessage", Payload: chatPayloadBytes}
			finalMsgBytes, _ := json.Marshal(finalMsg)
			c.hub.broadcast <- finalMsgBytes

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

			// Check if everyone is ready to start the game
			c.hub.mu.Lock()
			allReady := true
			identifiedAndReadyCount := 0
			totalIdentifiedCount := 0
			for client := range c.hub.clients {
				client.mu.Lock()
				if client.username != "" {
					totalIdentifiedCount++
					if !client.isReady {
						allReady = false
					} else {
						identifiedAndReadyCount++
					}
				}
				client.mu.Unlock()
			}
			c.hub.mu.Unlock()

			if totalIdentifiedCount > 0 && allReady {
				log.Println("Todos los jugadores identificados están listos. Iniciando juego.")
				c.hub.startGame()
			} else {
				log.Printf("No todos listos. Listos: %d/%d. Total clientes: %d", identifiedAndReadyCount, totalIdentifiedCount, len(c.hub.clients))
			}

		case "requestStartGame": // Mensaje enviado por Gandalf para iniciar solo
			c.mu.Lock()
			isGandalf := c.username == "Gandalf"
			isGandalfReady := c.isReady
			c.mu.Unlock()

			if isGandalf && isGandalfReady {
				c.hub.mu.Lock()
				numIdentified := 0
				for cl := range c.hub.clients {
					cl.mu.Lock()
					if cl.username != "" {
						numIdentified++
					}
					cl.mu.Unlock()
				}
				c.hub.mu.Unlock()
				if numIdentified >= 1 { // Gandalf puede iniciar si hay al menos 1 (él mismo)
					log.Printf("Gandalf (listo) solicitó iniciar el juego. Jugadores identificados: %d", numIdentified)
					c.hub.startGame()
				}
			}

		case "wordChosenByDrawer":
			var payload WordChosenPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar WordChosenPayload: %v", err)
				continue
			}

			c.hub.gameState.mu.Lock()
			if c.username == c.hub.gameState.currentDrawerUsername {
				c.hub.gameState.currentWord = payload.ChosenWord
				log.Printf("Dibujante %s eligió la palabra: %s", c.username, payload.ChosenWord)

				// Notify everyone that the drawing phase begins
				// The dibujante receives the word; the adivinadores do not.
				drawingPhaseDuration := 90 // segundos

				// Message for the dibujante
				drawerStartPayload := DrawingPhaseStartPayload{
					DrawerUsername: c.hub.gameState.currentDrawerUsername,
					WordToDraw:     c.hub.gameState.currentWord,
					Duration:       drawingPhaseDuration,
				}
				drawerStartPayloadBytes, _ := json.Marshal(drawerStartPayload)
				drawerStartMsg := Message{Type: "drawingPhaseStart", Payload: drawerStartPayloadBytes}
				drawerStartMsgBytes, _ := json.Marshal(drawerStartMsg)
				c.send <- drawerStartMsgBytes // Send only to the dibujante

				// Message for the adivinadores
				guesserStartPayload := DrawingPhaseStartPayload{
					DrawerUsername: c.hub.gameState.currentDrawerUsername,
					Duration:       drawingPhaseDuration,
					// WordToDraw is ignored for adivinadores
				}
				guesserStartPayloadBytes, _ := json.Marshal(guesserStartPayload)
				guesserStartMsg := Message{Type: "drawingPhaseStart", Payload: guesserStartPayloadBytes}
				guesserStartMsgBytes, _ := json.Marshal(guesserStartMsg)

				c.hub.mu.Lock() // Block hub to iterate on clients
				for clientItem := range c.hub.clients {
					if clientItem != c { // Do not send the dibujante again
						clientItem.send <- guesserStartMsgBytes
					}
				}
				c.hub.mu.Unlock()

			} else {
				log.Printf("Usuario %s intentó elegir palabra pero no es el dibujante (%s)", c.username, c.hub.gameState.currentDrawerUsername)
			}
			c.hub.gameState.mu.Unlock()

		case "drawAction":
			c.hub.gameState.mu.Lock()
			// Only the current dibujante can submit drawing actions.
			if c.username == c.hub.gameState.currentDrawerUsername && c.hub.gameState.gameInProgress {
				// Repackage the message to ensure the type is correct and add username if necessary

				// Extract the original payload from DrawActionPayload
				var drawPayload DrawActionPayload
				if err := json.Unmarshal(msg.Payload, &drawPayload); err != nil {
					log.Printf("Error al decodificar DrawActionPayload: %v", err)
					c.hub.gameState.mu.Unlock()
					continue
				}
				drawPayload.Username = c.username // Ensure the dibujante's username is in the payload

				// Re-serialize the payload
				enrichedPayloadBytes, err := json.Marshal(drawPayload)
				if err != nil {
					log.Printf("Error al re-serializar DrawActionPayload enriquecido: %v", err)
					c.hub.gameState.mu.Unlock()
					continue
				}

				// Create the final message for broadcast
				drawMsg := Message{Type: "drawAction", Payload: enrichedPayloadBytes}
				drawMsgBytes, err := json.Marshal(drawMsg)
				if err != nil {
					log.Printf("Error al serializar mensaje drawAction final: %v", err)
					c.hub.gameState.mu.Unlock()
					continue
				}

				// Broadcast to all clients (including the artist, so everyone sees the same thing)
				c.hub.mu.Lock()
				for clientItem := range c.hub.clients {
					// if clientItem != c { // Opción: no enviar de vuelta al dibujante
					select {
					case clientItem.send <- drawMsgBytes:
					default:
						log.Printf("Canal de envío bloqueado para %s al retransmitir drawAction", clientItem.username)
					}
					// }
				}
				c.hub.mu.Unlock()

			} else {
				log.Printf("Usuario %s intentó enviar drawAction pero no es el dibujante (%s) o juego no activo.", c.username, c.hub.gameState.currentDrawerUsername)
			}
			c.hub.gameState.mu.Unlock()

		default:
			log.Printf("Tipo de mensaje desconocido de %s: %s", c.username, msg.Type)
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
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("Error de escritura de WebSocket para %s: %v", c.username, err)
				return
			}

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
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256)}
	client.hub.register <- client

	// Allow the goroutine state collection when the function returns.
	go client.writePump()
	go client.readPump()
	log.Println("Nuevo cliente WebSocket conectado (IP:", r.RemoteAddr, ")")
}

func main() {
	hub := newHub()
	go hub.run() // Start the hub in a separate goroutine

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})

	port := "8080"
	log.Printf("Servidor WebSocket Pizarreada escuchando en el puerto %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
