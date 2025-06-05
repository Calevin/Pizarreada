package game

import (
	"encoding/json"
	"log"
	"math/rand"
	"pizarreada_backend/internal/utils"
	"sort"
	"time"
)

func (h *Hub) startGame() {
	h.gameState.mu.Lock()
	defer h.gameState.mu.Unlock()

	if h.gameState.gameInProgress {
		log.Println("Intento de iniciar juego, pero ya está en progreso.")
		return
	}
	h.mu.Lock()
	var readyPlayers []*Client
	h.gameState.scores = make(map[string]int)           // Reset scores for a new game
	h.gameState.playerDrawCounts = make(map[string]int) // Reset draw counts

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

	h.gameState.totalTurnsCompleted++ // Increment before checking to count the current turn

	if h.gameState.totalTurnsCompleted > h.gameState.maxTurns {
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

	h.gameState.currentWord = ""                                    // Reset current word
	h.gameState.playersWhoGuessedThisTurn = make(map[string]bool)   // Reset for the new turn
	h.gameState.currentTurnDrawActions = make([]json.RawMessage, 0) // Clear drawing history

	log.Printf("Asignando dibujante: %s (Turno %d/%d, Dibujo #%d para este jugador)", h.gameState.currentDrawerUsername, h.gameState.totalTurnsCompleted, h.gameState.maxTurns, h.gameState.playerDrawCounts[h.gameState.currentDrawerUsername])
	wordsToChoose := h.getRandomWords(3)

	// Start timer for word selection
	if h.gameState.wordSelectionTimer != nil {
		h.gameState.wordSelectionTimer.Stop()
	}
	h.gameState.wordSelectionTimer = time.NewTimer(wordSelectionDuration)
	go func(drawerName string, currentTurn int) {
		<-h.gameState.wordSelectionTimer.C
		h.gameState.mu.Lock()
		defer h.gameState.mu.Unlock()
		// If the dibujante did not choose a word and it is still his turn to choose
		if h.gameState.gameInProgress && h.gameState.currentDrawerUsername == drawerName && h.gameState.currentWord == "" && h.gameState.totalTurnsCompleted == currentTurn {
			log.Printf("Tiempo agotado para %s para elegir palabra. Eligiendo al azar.", drawerName)
			randomWord := wordsToChoose[rand.Intn(len(wordsToChoose))]
			// Simulate that the cartoonist chose this word
			h.handleWordChosen(drawerName, randomWord)
		}
	}(h.gameState.currentDrawerUsername, h.gameState.totalTurnsCompleted)

	drawerPayload := AssignDrawerPayload{
		DrawerUsername:      h.gameState.currentDrawerUsername,
		WordsToChoose:       wordsToChoose,
		Duration:            int(wordSelectionDuration.Seconds()),
		TotalTurnsCompleted: h.gameState.totalTurnsCompleted,
		MaxTurns:            h.gameState.maxTurns,
	}
	drawerPayloadBytes, _ := json.Marshal(drawerPayload)
	drawerMsg := Message{Type: "assignDrawerAndWords", Payload: drawerPayloadBytes}
	drawerMsgBytes, _ := json.Marshal(drawerMsg)

	// Send only to the drawing client
	select {
	case drawerClient.Send <- drawerMsgBytes:
		log.Printf("Mensaje assignDrawerAndWords enviado a %s", drawerClient.username)
	default:
		log.Printf("Error al enviar assignDrawerAndWords a %s: canal bloqueado", drawerClient.username)
	}

	guesserPayload := AssignDrawerPayload{
		DrawerUsername:      h.gameState.currentDrawerUsername,
		TotalTurnsCompleted: h.gameState.totalTurnsCompleted,
		MaxTurns:            h.gameState.maxTurns,
	}
	guesserPayloadBytes, _ := json.Marshal(guesserPayload)
	guesserMsg := Message{Type: "assignDrawerAndWords", Payload: guesserPayloadBytes}
	guesserMsgBytes, _ := json.Marshal(guesserMsg)

	h.mu.Lock()
	for client := range h.clients {
		if client != drawerClient {
			select {
			case client.Send <- guesserMsgBytes:
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
		drawerClient.Send <- drawerStartMsgBytes
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
			clientItem.Send <- guesserStartMsgBytes
		}
	}
	h.mu.Unlock()

	// Start drawing timer
	if h.gameState.turnTimer != nil {
		h.gameState.turnTimer.Stop()
	}
	h.gameState.turnTimer = time.NewTimer(drawingDuration)
	go func(currentWordForTimer string, turnNumber int) {
		<-h.gameState.turnTimer.C
		h.gameState.mu.Lock()
		defer h.gameState.mu.Unlock()
		// Make sure the turn has not changed
		if h.gameState.gameInProgress && h.gameState.currentWord == currentWordForTimer && h.gameState.totalTurnsCompleted == turnNumber {
			log.Printf("Tiempo de dibujo agotado para %s. Palabra: %s", h.gameState.currentDrawerUsername, currentWordForTimer)
			h.broadcastTurnOver("Tiempo agotado", currentWordForTimer)
			h.assignNextDrawer()
		}
	}(h.gameState.currentWord, h.gameState.totalTurnsCompleted)
}

// getRandomWords returns a list of words from the gameWords list.
func (h *Hub) getRandomWords(count int) []string {
	if len(h.gameWords) == 0 {
		return []string{"default1", "default2", "default3"} // Fallback
	}
	// Create a copy for shuffling, so as not to permanently modify h.gameWords
	wordsCopy := make([]string, len(h.gameWords))
	copy(wordsCopy, h.gameWords)

	rand.Shuffle(len(wordsCopy), func(i, j int) { wordsCopy[i], wordsCopy[j] = wordsCopy[j], wordsCopy[i] })

	numToPick := count
	if len(wordsCopy) < count {
		numToPick = len(wordsCopy)
	}
	return wordsCopy[:numToPick]
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
func (h *Hub) handleGuessAttempt(guesserClient *Client, guess string) (isCorrectGuess bool) {
	h.gameState.mu.Lock()
	defer h.gameState.mu.Unlock()

	isCorrectGuess = false

	if !h.gameState.gameInProgress || guesserClient.username == h.gameState.currentDrawerUsername || h.gameState.playersWhoGuessedThisTurn[guesserClient.username] {
		return // Game not active, dibujante cannot guess, or has already guessed
	}

	normalizedGuess := utils.NormalizeString(guess)
	normalizedCurrentWord := utils.NormalizeString(h.gameState.currentWord)

	if normalizedGuess == normalizedCurrentWord {
		isCorrectGuess = true
		log.Printf("¡Adivinanza correcta de %s! Palabra: %s", guesserClient.username, h.gameState.currentWord)
		h.gameState.playersWhoGuessedThisTurn[guesserClient.username] = true

		// Calculate points
		pointsForGuesser := 0

		// Count only active players in the current game (in playerOrder)
		// that are not the current cartoonist
		activeGuessersCount := 0

		h.mu.Lock() // Lock the hub to safely iterate over h.clients
		for c := range h.clients {
			c.mu.Lock()
			// A player is an "active Adivinador" if:
			// 1. The adivinador has a username (is identified).
			// 2. He is NOT the current dibujante.
			// 3. It is ready (isReady)
			if c.username != "" && c.username != h.gameState.currentDrawerUsername && c.isReady {
				activeGuessersCount++
			}
			c.mu.Unlock()
		}
		h.mu.Unlock() // Unlock the hub

		if activeGuessersCount <= 0 { // This should not happen if the game is in progress
			pointsForGuesser = 1
		} else {
			// Points = total adivinadores - (how many already guessed before this one) + 1
			// Base points is the number of adivinadores.
			// The -1 on playersWhoGuessedThisTurn is because the current one was already included
			alreadyGuessedCount := len(h.gameState.playersWhoGuessedThisTurn) - 1
			pointsForGuesser = activeGuessersCount - alreadyGuessedCount
			if pointsForGuesser < 1 {
				pointsForGuesser = 1
			}
		}

		h.gameState.scores[guesserClient.username] += pointsForGuesser
		h.gameState.scores[h.gameState.currentDrawerUsername] += 1 //1 point for the dibujante for each riddle

		h.broadcastScoreUpdate()

		turnOverByGuess := h.checkIfAllGuessed() // This function can also end the turn

		correctGuessPayload := GuessCorrectPayload{
			GuesserUsername: guesserClient.username,
			PointsGuesser:   pointsForGuesser,
			PointsDrawer:    1, // Points the dibujante earns for THIS riddle
			IsTurnOver:      turnOverByGuess,
		}
		if turnOverByGuess {
			correctGuessPayload.WordRevealed = h.gameState.currentWord
		}
		payloadBytes, _ := json.Marshal(correctGuessPayload)
		msg := Message{Type: "guessCorrect", Payload: payloadBytes}
		msgBytes, _ := json.Marshal(msg)
		h.broadcast <- msgBytes // Notify everyone of the correct guess

		// If the turn ended because everyone guessed, assignNextDrawer was already called by checkIfAllGuessed
	}
	return
}

// checkIfAllGuessed check if all the adivinadores have guessed correctly.
// Returns true if the turn ended as a result, false otherwise.
// gameState.mu MUST be blocked before calling.
func (h *Hub) checkIfAllGuessed() bool {
	// gameState.mu should be blocked by now
	if !h.gameState.gameInProgress {
		return false
	}

	numPotentialGuessers := 0
	h.mu.Lock() // Lock the hub to safely iterate over h.clients
	for client := range h.clients {
		client.mu.Lock()
		// A player is a "potential adivinador" if:
		// 1. The adivinador has a username (is identified).
		// 2. He is NOT the current dibujante.
		// 3. It is ready (isReady)
		if client.username != "" && client.username != h.gameState.currentDrawerUsername && client.isReady {
			numPotentialGuessers++
		}
		client.mu.Unlock()
	}
	h.mu.Unlock() // Unlock the hub

	if len(h.gameState.playersWhoGuessedThisTurn) >= numPotentialGuessers && numPotentialGuessers > 0 {
		log.Printf("Todos los %d adivinadores han acertado. Terminando turno.", numPotentialGuessers)
		if h.gameState.turnTimer != nil {
			h.gameState.turnTimer.Stop()
		}
		h.broadcastTurnOver("Todos adivinaron", h.gameState.currentWord)
		h.assignNextDrawer()
		return true
	}
	return false
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
