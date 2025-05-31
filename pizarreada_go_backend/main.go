package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

const (
	wordSelectionDuration = 20 * time.Second
	drawingDuration       = 90 * time.Second
	turnsPerPlayer        = 3 // Number of times each player draws before the game ends
)

// GameState represents the current state of the game.
type GameState struct {
	currentDrawerUsername     string
	currentWord               string
	gameInProgress            bool
	playerOrder               []*Client // To determine the next Dibujante
	currentPlayerIndex        int       // Index in playerOrder of the current Dibujante
	scores                    map[string]int
	playersWhoGuessedThisTurn map[string]bool // Usernames of those who guessed
	turnTimer                 *time.Timer     // Timer for drawing turn
	wordSelectionTimer        *time.Timer     // Timer for word selection
	playerDrawCounts          map[string]int  // Times each player has drawn
	totalTurnsCompleted       int
	maxTurns                  int // It will be calculated based on players and turnsPerPlayer
	mu                        sync.Mutex
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
	Duration       int      `json:"duration,omitempty"`      // Time to choose a word
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

// GuessCorrectPayload to notify a correct guess.
type GuessCorrectPayload struct {
	GuesserUsername string `json:"guesserUsername"`
	PointsGuesser   int    `json:"pointsGuesser"`
	PointsDrawer    int    `json:"pointsDrawer"`           // Points the dibujante earns for this riddle
	IsTurnOver      bool   `json:"isTurnOver"`             // If this was the last one to guess
	WordRevealed    string `json:"wordRevealed,omitempty"` // The word, if the turn is over
}

// ScoreUpdatePayload to send score updates.
type ScoreUpdatePayload struct {
	Scores map[string]int `json:"scores"`
}

// GameOverPayload to notify the end of the game.
type GameOverPayload struct {
	Scores      map[string]int `json:"scores"`
	Reason      string         `json:"reason"`
	SortedUsers []string       `json:"sortedUsers"` // Usernames sorted by score
}

// TurnOverPayload to notify that the turn has ended (e.g. due to time).
type TurnOverPayload struct {
	WordRevealed string `json:"wordRevealed"`
	Reason       string `json:"reason"` // "time" or "everyone guessed"
}

func newGameState() *GameState {
	return &GameState{
		gameInProgress:     false,
		currentPlayerIndex: -1, // No one is drawing initially
		scores:             make(map[string]int),
		playerDrawCounts:   make(map[string]int),
	}
}

func newHub() *Hub {
	rand.Seed(time.Now().UnixNano()) // Initialize random number generator
	return &Hub{
		broadcast:  make(chan []byte),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		gameState:  newGameState(),
		gameWords:  []string{"perro", "casa", "pelota", "silla", "dragon", "arbol", "sol", "luna", "estrella", "rio", "montaña", "flor", "puente", "llave", "libro", "nube", "barco", "tren"},
	}
}

// normalizeString converts to lowercase and removes accents.
func normalizeString(s string) string {
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, s)
	return result
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
			username := client.username // Save before the client leaves
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
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
						h.gameState.totalTurnsCompleted++
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

// broadcastScoreUpdate sends the current score to all clients.
func (h *Hub) broadcastScoreUpdate() {
	// gameState.mu should already be blocked by the caller
	scoresCopy := make(map[string]int)
	for k, v := range h.gameState.scores {
		scoresCopy[k] = v
	}
	payloadBytes, _ := json.Marshal(ScoreUpdatePayload{Scores: scoresCopy})
	msg := Message{Type: "scoreUpdate", Payload: payloadBytes}
	msgBytes, _ := json.Marshal(msg)
	h.broadcast <- msgBytes // Usar el canal de broadcast del hub
	log.Println("Actualización de puntajes transmitida.")
}

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
			// Initialize scores and draw count for incoming players
			if _, ok := h.gameState.scores[client.username]; !ok {
				h.gameState.scores[client.username] = 0
			}
			if _, ok := h.gameState.playerDrawCounts[client.username]; !ok {
				h.gameState.playerDrawCounts[client.username] = 0
			}
		}
		client.mu.Unlock()
	}
	h.mu.Unlock()

	if len(readyPlayers) < 1 { // At least 1 player is required (Gandalf can play alone)
		log.Println("No hay suficientes jugadores listos para empezar el juego.")
		return
	}

	// Mix up the order of players taking turns
	rand.Shuffle(len(readyPlayers), func(i, j int) {
		readyPlayers[i], readyPlayers[j] = readyPlayers[j], readyPlayers[i]
	})

	h.gameState.playerOrder = readyPlayers
	h.gameState.currentPlayerIndex = -1 // It will be incremented to 0 on assignNextDrawer the first time
	h.gameState.gameInProgress = true
	h.gameState.totalTurnsCompleted = 0
	h.gameState.maxTurns = len(readyPlayers) * turnsPerPlayer
	log.Printf("Juego iniciado. Orden de jugadores: %v. Max Turns: %d", getPlayerUsernames(h.gameState.playerOrder), h.gameState.maxTurns)

	h.broadcastScoreUpdate() // Submit initial scores (all 0)
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
		if h.gameState.gameInProgress { // Only if the game was active
			h.gameState.gameInProgress = false // Ensure that the game does not continue
			h.broadcastGameEnd("No hay jugadores para continuar.")
		}
		return
	}

	if h.gameState.totalTurnsCompleted >= h.gameState.maxTurns {
		log.Println("Todas las rondas completadas. Fin del juego.")
		h.gameState.gameInProgress = false
		h.broadcastGameEnd("Todas las rondas completadas.")
		return
	}

	h.gameState.currentPlayerIndex = (h.gameState.currentPlayerIndex + 1) % len(h.gameState.playerOrder)
	drawerClient := h.gameState.playerOrder[h.gameState.currentPlayerIndex]

	drawerClient.mu.Lock()
	h.gameState.currentDrawerUsername = drawerClient.username
	h.gameState.playerDrawCounts[drawerClient.username]++ // Increase drawing count for this player
	drawerClient.mu.Unlock()

	h.gameState.currentWord = ""                                  // Reset current word
	h.gameState.playersWhoGuessedThisTurn = make(map[string]bool) // Reset for the new turn

	log.Printf("Asignando dibujante: %s (Turno %d/%d, Dibujo #%d para este jugador)", h.gameState.currentDrawerUsername, h.gameState.totalTurnsCompleted+1, h.gameState.maxTurns, h.gameState.playerDrawCounts[h.gameState.currentDrawerUsername])
	wordsToChoose := h.getRandomWords(3)

	// Start timer for word selection
	if h.gameState.wordSelectionTimer != nil {
		h.gameState.wordSelectionTimer.Stop()
	}
	h.gameState.wordSelectionTimer = time.NewTimer(wordSelectionDuration)
	go func(drawerName string) {
		<-h.gameState.wordSelectionTimer.C
		h.gameState.mu.Lock()
		defer h.gameState.mu.Unlock()
		// If the dibujante did not choose a word and it is still his turn to choose
		if h.gameState.gameInProgress && h.gameState.currentDrawerUsername == drawerName && h.gameState.currentWord == "" {
			log.Printf("Tiempo agotado para %s para elegir palabra. Eligiendo al azar.", drawerName)
			randomWord := wordsToChoose[rand.Intn(len(wordsToChoose))]
			// Simulate that the cartoonist chose this word
			h.handleWordChosen(drawerName, randomWord)
		}
	}(h.gameState.currentDrawerUsername)

	drawerPayload := AssignDrawerPayload{
		DrawerUsername: h.gameState.currentDrawerUsername,
		WordsToChoose:  wordsToChoose,
		Duration:       int(wordSelectionDuration.Seconds()),
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

	guesserPayload := AssignDrawerPayload{DrawerUsername: h.gameState.currentDrawerUsername}
	guesserPayloadBytes, _ := json.Marshal(guesserPayload)
	guesserMsg := Message{Type: "assignDrawerAndWords", Payload: guesserPayloadBytes}
	guesserMsgBytes, _ := json.Marshal(guesserMsg)

	h.mu.Lock()
	for client := range h.clients {
		if client != drawerClient {
			select {
			case client.send <- guesserMsgBytes:
			default:
				log.Printf("Error al enviar assignDrawerAndWords (adivinador) a %s: canal bloqueado", client.username)
			}
		}
	}
	h.mu.Unlock()
}

// handleWordChosen is called when a word is chosen by the dibujante.
func (h *Hub) handleWordChosen(drawerUsername string, chosenWord string) {
	// gameState.mu should be blocked by now
	if !h.gameState.gameInProgress || h.gameState.currentDrawerUsername != drawerUsername || h.gameState.currentWord != "" {
		// The word has already been chosen, OR it's not the dibujante's turn.
		return
	}
	if h.gameState.wordSelectionTimer != nil { // Stop selection timer if exists
		h.gameState.wordSelectionTimer.Stop()
	}

	h.gameState.currentWord = chosenWord
	log.Printf("Dibujante %s eligió la palabra: %s", drawerUsername, chosenWord)

	// Start drawing phase
	drawingPhaseDurationSeconds := int(drawingDuration.Seconds())
	drawerStartPayload := DrawingPhaseStartPayload{
		DrawerUsername: h.gameState.currentDrawerUsername,
		WordToDraw:     h.gameState.currentWord,
		Duration:       drawingPhaseDurationSeconds,
	}
	drawerStartPayloadBytes, _ := json.Marshal(drawerStartPayload)
	drawerStartMsg := Message{Type: "drawingPhaseStart", Payload: drawerStartPayloadBytes}
	drawerStartMsgBytes, _ := json.Marshal(drawerStartMsg)

	// Find the dibujante client to send the message to
	var drawerClient *Client
	h.mu.Lock() // Block hub from accessing h.clients
	for c := range h.clients {
		c.mu.Lock()
		if c.username == drawerUsername {
			drawerClient = c
		}
		c.mu.Unlock()
	}
	h.mu.Unlock()

	if drawerClient != nil {
		drawerClient.send <- drawerStartMsgBytes
	}

	guesserStartPayload := DrawingPhaseStartPayload{
		DrawerUsername: h.gameState.currentDrawerUsername,
		Duration:       drawingPhaseDurationSeconds,
	}
	guesserStartPayloadBytes, _ := json.Marshal(guesserStartPayload)
	guesserStartMsg := Message{Type: "drawingPhaseStart", Payload: guesserStartPayloadBytes}
	guesserStartMsgBytes, _ := json.Marshal(guesserStartMsg)

	h.mu.Lock()
	for clientItem := range h.clients {
		if clientItem != drawerClient {
			clientItem.send <- guesserStartMsgBytes
		}
	}
	h.mu.Unlock()

	// Start drawing timer
	if h.gameState.turnTimer != nil {
		h.gameState.turnTimer.Stop()
	}
	h.gameState.turnTimer = time.NewTimer(drawingDuration)
	go func(currentWordForTimer string) {
		<-h.gameState.turnTimer.C
		h.gameState.mu.Lock()
		defer h.gameState.mu.Unlock()
		if h.gameState.gameInProgress && h.gameState.currentWord == currentWordForTimer { // Make sure the turn has not changed
			log.Printf("Tiempo de dibujo agotado para %s. Palabra: %s", h.gameState.currentDrawerUsername, currentWordForTimer)
			h.broadcastTurnOver("Tiempo agotado", currentWordForTimer)
			h.gameState.totalTurnsCompleted++
			h.assignNextDrawer()
		}
	}(h.gameState.currentWord)
}

// getRandomWords returns a list of words from the gameWords list.
func (h *Hub) getRandomWords(count int) []string {
	if len(h.gameWords) == 0 {
		return []string{"default1", "default2", "default3"} // Fallback
	}
	rand.Shuffle(len(h.gameWords), func(i, j int) { h.gameWords[i], h.gameWords[j] = h.gameWords[j], h.gameWords[i] })
	numToPick := count
	if len(h.gameWords) < count {
		numToPick = len(h.gameWords)
	}
	return h.gameWords[:numToPick]
}

// broadcastGameEnd sends a message to all clients indicating the end of the game.
// reason is a string describing the reason for the game ending.
func (h *Hub) broadcastGameEnd(reason string) {
	// gameState.mu should be blocked by now
	log.Println("Transmitiendo fin de juego:", reason)

	scoresCopy := make(map[string]int)
	var sortedUsernames []string
	for uname, score := range h.gameState.scores {
		scoresCopy[uname] = score
		sortedUsernames = append(sortedUsernames, uname)
	}

	// Sort users by descending score
	sort.SliceStable(sortedUsernames, func(i, j int) bool {
		return scoresCopy[sortedUsernames[i]] > scoresCopy[sortedUsernames[j]]
	})

	payload := GameOverPayload{
		Scores:      scoresCopy,
		Reason:      reason,
		SortedUsers: sortedUsernames,
	}
	payloadBytes, _ := json.Marshal(payload)
	msg := Message{Type: "gameOver", Payload: payloadBytes}
	msgBytes, _ := json.Marshal(msg)
	h.broadcast <- msgBytes

	// Reset game state for a possible new game
	h.gameState.gameInProgress = false
	h.gameState.currentDrawerUsername = ""
	h.gameState.currentWord = ""
	h.gameState.playerOrder = nil
	h.gameState.currentPlayerIndex = -1
	h.gameState.scores = make(map[string]int)
	h.gameState.playerDrawCounts = make(map[string]int)
	h.gameState.totalTurnsCompleted = 0
	if h.gameState.turnTimer != nil {
		h.gameState.turnTimer.Stop()
	}
	if h.gameState.wordSelectionTimer != nil {
		h.gameState.wordSelectionTimer.Stop()
	}

	// Set all players as not ready
	h.mu.Lock()
	for client := range h.clients {
		client.mu.Lock()
		client.isReady = false
		client.mu.Unlock()
	}
	h.mu.Unlock()
	h.broadcastUserList() // Update lobby UI
}

// broadcastTurnOver sends a message to all clients indicating that the turn has ended.
// reason is a string describing the reason for the turn ending.
// word is the word that was revealed.
func (h *Hub) broadcastTurnOver(reason string, word string) {
	// gameState.mu should be blocked by now
	log.Printf("Turno terminado: %s. Palabra: %s", reason, word)
	payload := TurnOverPayload{Reason: reason, WordRevealed: word}
	payloadBytes, _ := json.Marshal(payload)
	msg := Message{Type: "turnOver", Payload: payloadBytes}
	msgBytes, _ := json.Marshal(msg)
	h.broadcast <- msgBytes
}

// handleGuessAttempt is called when a guess is made by a client.
func (h *Hub) handleGuessAttempt(guesserClient *Client, guess string) {
	h.gameState.mu.Lock()
	defer h.gameState.mu.Unlock()

	if !h.gameState.gameInProgress || guesserClient.username == h.gameState.currentDrawerUsername || h.gameState.playersWhoGuessedThisTurn[guesserClient.username] {
		return // Game not active, dibujante cannot guess, or has already guessed
	}

	normalizedGuess := normalizeString(guess)
	normalizedCurrentWord := normalizeString(h.gameState.currentWord)

	if normalizedGuess == normalizedCurrentWord {
		log.Printf("¡Adivinanza correcta de %s! Palabra: %s", guesserClient.username, h.gameState.currentWord)
		h.gameState.playersWhoGuessedThisTurn[guesserClient.username] = true

		// Calcular puntos
		pointsForGuesser := 0
		h.mu.Lock() // To count active players (adivinadores)
		activeGuessers := 0
		for c := range h.clients {
			c.mu.Lock()
			if c.username != "" && c.username != h.gameState.currentDrawerUsername {
				activeGuessers++
			}
			c.mu.Unlock()
		}
		h.mu.Unlock()
		if activeGuessers == 0 {
			activeGuessers = 1
		} // Avoid division by zero if only the dibujante is present (Gandalf mode)

		// Points = total advinadores - (how many already guessed before this one) + 1
		// Base points is the number of adivinadores.
		// The first adivinador gets `activeGuessers` points.
		// The second `activeGuessers - 1`, etc., minimum 1.
		pointsForGuesser = activeGuessers - (len(h.gameState.playersWhoGuessedThisTurn) - 1)
		if pointsForGuesser < 1 {
			pointsForGuesser = 1
		}

		h.gameState.scores[guesserClient.username] += pointsForGuesser
		h.gameState.scores[h.gameState.currentDrawerUsername] += 1 //1 point for the dibujante for each riddle

		h.broadcastScoreUpdate()

		isTurnOver := h.checkIfAllGuessed() // This function can also end the turn

		correctGuessPayload := GuessCorrectPayload{
			GuesserUsername: guesserClient.username,
			PointsGuesser:   pointsForGuesser,
			PointsDrawer:    1, // Points the dibujante earns for THIS riddle
			IsTurnOver:      isTurnOver,
		}
		if isTurnOver {
			correctGuessPayload.WordRevealed = h.gameState.currentWord
		}
		payloadBytes, _ := json.Marshal(correctGuessPayload)
		msg := Message{Type: "guessCorrect", Payload: payloadBytes}
		msgBytes, _ := json.Marshal(msg)
		h.broadcast <- msgBytes // Notify everyone of the correct guess

		// If the turn ended because everyone guessed, assignNextDrawer was already called by checkIfAllGuessed
	}
}

// checkIfAllGuessed check if all the adivinadores have guessed correctly.
// Returns true if the turn ended as a result, false otherwise.
// gameState.mu MUST be blocked before calling.
func (h *Hub) checkIfAllGuessed() bool {
	if !h.gameState.gameInProgress {
		return false
	}
	h.mu.Lock() // To count clients
	numPotentialGuessers := 0
	for client := range h.clients {
		client.mu.Lock()
		if client.username != "" && client.username != h.gameState.currentDrawerUsername {
			numPotentialGuessers++
		}
		client.mu.Unlock()
	}
	h.mu.Unlock()

	if numPotentialGuessers == 0 && len(h.gameState.playerOrder) == 1 { // Gandalf case only
		numPotentialGuessers = 1 // Gandalf is his own adivinador
	}

	if len(h.gameState.playersWhoGuessedThisTurn) >= numPotentialGuessers && numPotentialGuessers > 0 {
		log.Printf("Todos los %d adivinadores han acertado. Terminando turno.", numPotentialGuessers)
		if h.gameState.turnTimer != nil {
			h.gameState.turnTimer.Stop()
		}
		h.broadcastTurnOver("Todos adivinaron", h.gameState.currentWord)
		h.gameState.totalTurnsCompleted++
		h.assignNextDrawer()
		return true
	}
	return false
}

// readPump pumps messages from the WebSocket connection to the hub.
func (c *Client) readPump() {
	defer func() { c.hub.unregister <- c; c.conn.Close() }()
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
			log.Printf("Error JSON de %s: %v. Msg: %s", c.username, err, string(rawMessage))
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
		case "chatMessage": // It can be a normal chat or a guessing attempt.
			log.Printf("chatMessage...")
			var payload ChatPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar ChatPayload: %v", err)
				continue
			}
			c.hub.gameState.mu.Lock()
			isGameChat := c.hub.gameState.gameInProgress && c.username != c.hub.gameState.currentDrawerUsername && !c.hub.gameState.playersWhoGuessedThisTurn[c.username]
			currentWordExists := c.hub.gameState.currentWord != ""
			c.hub.gameState.mu.Unlock()

			if isGameChat && currentWordExists { // It's an attempt at a guess
				c.hub.handleGuessAttempt(c, payload.Message)
			}
			// Always retransmit the chat message (even if it was a guess, so it can be seen)
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
			log.Printf("User %s ready: %t", c.username, payload.IsReady)
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
			c.hub.handleWordChosen(c.username, payload.ChosenWord)
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
