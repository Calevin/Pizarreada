package game

import (
	"encoding/json"
	"log"
	"math/rand"
	"sync"
	"time"
)

const (
	wordSelectionDuration = 20 * time.Second
	drawingDuration       = 90 * time.Second
	turnsPerPlayer        = 3 // Number of times each player draws before the game ends
)

// Hub maintains the active client pool and transmits messages to clients.
type Hub struct {
	clients    map[*Client]bool // Registered clients.
	broadcast  chan []byte      // Incoming messages from clients.
	Register   chan *Client     // Channel to Register clients.
	unregister chan *Client     // Channel to unregister the client.
	mu         sync.Mutex       // To protect client access.
	gameState  *GameState       // Game state
	gameWords  []string         // Words available for the game
}

func NewHub() *Hub {
	rand.Seed(time.Now().UnixNano()) // Initialize random number generator
	return &Hub{
		broadcast:  make(chan []byte),
		Register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		gameState:  newGameState(),
		gameWords:  []string{"perro", "casa", "pelota", "silla", "dragon", "arbol", "sol", "luna", "estrella", "rio", "montaña", "flor", "puente", "llave", "libro", "nube", "barco", "tren"},
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.Register:
			h.mu.Lock()
			h.clients[client] = true
			log.Printf("Cliente registrado (Total: %d)", len(h.clients))
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			username := client.username // Save before the client leaves
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.Send)
				log.Printf("Cliente desconectado: %s (Total: %d)", username, len(h.clients))
			}
			h.mu.Unlock()
			if username != "" {
				h.broadcastUserList()
				// If a player leaves during the game (e.g. end turn, choose the new dibujante)
				h.gameState.mu.Lock()
				if h.gameState.gameInProgress {
					// If the leaving player was the current dibujante
					if h.gameState.currentDrawerUsername == username {
						log.Printf("El dibujante %s se desconectó. Terminando turno.", username)
						if h.gameState.turnTimer != nil {
							h.gameState.turnTimer.Stop()
						}
						h.broadcastTurnOver("El dibujante se desconectó.", h.gameState.currentWord)
						h.assignNextDrawer() // Try to move on to the next one
					} else {
						// If you were a adivinador, check if everyone else has already guessed.
						delete(h.gameState.playersWhoGuessedThisTurn, username) // Remove it from those who guessed
						h.checkIfAllGuessed()
					}
				}
				// Remove from playerOrder if it was
				var newPlayerOrder []*Client
				for _, p := range h.gameState.playerOrder {
					if p != client {
						newPlayerOrder = append(newPlayerOrder, p)
					}
				}
				h.gameState.playerOrder = newPlayerOrder
				// If the playerOrder is empty and the game was in progress, end it.
				if len(h.gameState.playerOrder) == 0 && h.gameState.gameInProgress {
					log.Println("No quedan jugadores. Terminando juego.")
					h.gameState.gameInProgress = false
					h.broadcastGameEnd("No quedan jugadores.")
				}

				h.gameState.mu.Unlock()
			}

		case message := <-h.broadcast:
			h.mu.Lock()
			for client := range h.clients {
				select {
				case client.Send <- message:
					// log.Printf("client.send %s <- message...", client.username)
				default:
					// If the sending channel is blocked, the client may be disconnected.
					close(client.Send)
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
		case client.Send <- msgBytes:
		default:
			log.Printf("Error al enviar userListUpdate a %s: canal de envío bloqueado.", client.username)
		}
	}
	// log.Println("Lista de usuarios transmitida:", string(msgBytes))
}
