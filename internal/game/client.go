package game

import (
	"encoding/json"
	"github.com/gorilla/websocket"
	"log"
	"sync"
)

// Message defines the structure of messages exchanged via WebSocket.
type Message struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Client represents a single connected WebSocket client.
type Client struct {
	Conn     *websocket.Conn
	Hub      *Hub
	Send     chan []byte // Channel to Send messages to the client.
	username string
	isReady  bool
	mu       sync.Mutex // To protect access to username and isReady
}

// ReadPump pumps messages from the WebSocket connection to the hub.
func (c *Client) ReadPump() {
	defer func() { c.Hub.unregister <- c; c.Conn.Close() }()
	for {
		_, rawMessage, err := c.Conn.ReadMessage()
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

		switch msg.Type {
		case "userIdentify":
			var payload UserPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar UserPayload: %v", err)
				continue
			}
			c.mu.Lock()
			alreadyExists := false
			// Check if the user already exists in the hub
			c.Hub.mu.Lock()
			for client := range c.Hub.clients {
				if client != c && client.username == payload.Username {
					alreadyExists = true
					break
				}
			}
			c.Hub.mu.Unlock()

			if alreadyExists {
				log.Printf("Intento de identificar usuario %s, pero ya existe.", payload.Username)
				c.mu.Unlock()
				continue
			}

			c.username = payload.Username
			c.isReady = false // By default it is not ready to log in
			c.mu.Unlock()
			log.Printf("Usuario identificado: %s", c.username)
			c.Hub.broadcastUserList() // Notify everyone about the new user/updated list

			c.Hub.gameState.mu.Lock()
			if c.Hub.gameState.gameInProgress {
				// If a game is in progress, a score update will be sent.
				// This ensures that the reconnecting player receives their current score.
				// And everyone else is in sync too.
				log.Printf("Juego en progreso, enviando actualización de puntajes tras identificación de %s.", payload.Username)
				c.Hub.broadcastScoreUpdate() // expect gameState.mu to be blocked.
			}
			c.Hub.gameState.mu.Unlock()

		case "chatMessage": // It can be a normal chat or a guessing attempt.
			var payload ChatPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar ChatPayload: %v", err)
				continue
			}

			isCorrectGuess := false
			c.Hub.gameState.mu.Lock()
			// Only allow guesses if the game is in progress, the user is NOT the artist,
			// AND The current word exists, AND the user has not yet guessed this turn.
			isGameChat := c.Hub.gameState.gameInProgress &&
				c.username != c.Hub.gameState.currentDrawerUsername &&
				c.Hub.gameState.currentWord != "" &&
				!c.Hub.gameState.playersWhoGuessedThisTurn[c.username]
			c.Hub.gameState.mu.Unlock() //Unlock before calling handleGuessAttempt which also blocks

			if isGameChat {
				isCorrectGuess = c.Hub.handleGuessAttempt(c, payload.Message)
			}

			if !isCorrectGuess { // Only rebroadcast if it was not a correct guess
				c.mu.Lock() // Required to read c.username securely
				payload.Username = c.username
				c.mu.Unlock()
				chatBytes, _ := json.Marshal(payload)
				finalMsg := Message{Type: "chatMessage", Payload: chatBytes}
				finalBytes, _ := json.Marshal(finalMsg)
				c.Hub.broadcast <- finalBytes
			}

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
			} else {
				// This should not happen if the client can only control its own state.
				log.Printf("Advertencia: Cliente %s intentó cambiar estado de listo para %s", c.username, payload.Username)
				c.mu.Unlock()
				continue
			}
			isNowReady := c.isReady
			currentPlayerUsername := c.username
			c.mu.Unlock()

			c.Hub.broadcastUserList() // Notify everyone about the status change and updated list

			c.Hub.gameState.mu.Lock()
			isGameCurrentlyInProgress := c.Hub.gameState.gameInProgress

			if isGameCurrentlyInProgress && isNowReady {
				// Player (c) has just become READY and there is a game IN PROGRESS.
				// Send status to join as an adivinador.
				currentDrawer := c.Hub.gameState.currentDrawerUsername
				turnsCompleted := c.Hub.gameState.totalTurnsCompleted
				maxTurnsGame := c.Hub.gameState.maxTurns
				// Copy drawing actions (It's best to copy the actions here while gameState.mu is locked)
				var actionsToSync []json.RawMessage
				if len(c.Hub.gameState.currentTurnDrawActions) > 0 {
					actionsToSync = make([]json.RawMessage, len(c.Hub.gameState.currentTurnDrawActions))
					copy(actionsToSync, c.Hub.gameState.currentTurnDrawActions)
				}

				c.Hub.gameState.mu.Unlock() // Unlock gameState here, we already have the data we need for assignDrawerAndWords and the actions.

				log.Printf("Jugador %s está listo (%t), juego en curso. Enviando estado para unirse.", currentPlayerUsername, isNowReady)

				// Create the payload. Do not include WordsToChoose or Duration for word selection.
				// The frontend in initTurn will handle this correctly if amIDrawer is false.
				guesserJoinPayload := AssignDrawerPayload{
					DrawerUsername:      currentDrawer,
					TotalTurnsCompleted: turnsCompleted,
					MaxTurns:            maxTurnsGame,
				}

				guesserJoinPayloadBytes, err := json.Marshal(guesserJoinPayload)
				if err != nil {
					log.Printf("Error al codificar guesserJoinPayload para %s: %v", currentPlayerUsername, err)
				} else {
					msgToJoin := Message{Type: "assignDrawerAndWords", Payload: guesserJoinPayloadBytes}
					msgToJoinBytes, err := json.Marshal(msgToJoin)
					if err != nil {
						log.Printf("Error al codificar mensaje final msgToJoin para %s: %v", currentPlayerUsername, err)
					} else {
						// Send the message only to the client 'c' that just got ready
						select {
						case c.Send <- msgToJoinBytes:
							log.Printf("Enviado assignDrawerAndWords a %s para unirse a partida en curso.", currentPlayerUsername)

							// Send drawing history after assignDrawerAndWords
							if len(actionsToSync) > 0 {
								log.Printf("Enviando %d acciones de dibujo acumuladas a %s.", len(actionsToSync), currentPlayerUsername)
								for i, actionPayload := range actionsToSync {
									// actionPayload is already a json.RawMessage
									syncDrawMsg := Message{Type: "drawAction", Payload: actionPayload}
									syncDrawMsgBytes, err := json.Marshal(syncDrawMsg)
									if err != nil {
										log.Printf("Error al serializar syncDrawMsg #%d para %s: %v", i+1, currentPlayerUsername, err)
										continue // Skip this action if there is a serialization error
									}

									// Send to client 'c'
									select {
									case c.Send <- syncDrawMsgBytes:
										// log.Printf("Enviada acción de dibujo acumulada #%d a %s", i+1, currentPlayerUsername) // Puede ser muy verboso
									default:
										log.Printf("Error al enviar acción de dibujo acumulada #%d a %s: canal bloqueado. Deteniendo sincronización de dibujo para este jugador.", i+1, currentPlayerUsername)
										// If the channel is blocked, the client has probably disconnected,
										goto endDrawingSyncForPlayer // Use goto to exit the action dispatch loop
									}
								}
							}
						endDrawingSyncForPlayer: // Tag for the goto
							log.Printf("Sincronización de dibujo completada para %s.", currentPlayerUsername)
						// End of sending drawing history

						default:
							log.Printf("Error al enviar assignDrawerAndWords a %s para unirse: canal bloqueado.", currentPlayerUsername)
						}
					}
				}

			} else if !isGameCurrentlyInProgress {
				// There is no game in progress. Check if a new one can be started.
				c.Hub.gameState.mu.Unlock() // Unlock gameState. The startup logic doesn't need to be locked initially.

				c.Hub.mu.Lock() // Block hub to count customers
				allReadyForNewGame := true
				identifiedAndReadyCount := 0
				totalIdentifiedCount := 0

				// Use a new variable for the iterator to avoid shadowing with 'c'
				var clientsForNewGameCheck []*Client
				for clientIter := range c.Hub.clients {
					clientsForNewGameCheck = append(clientsForNewGameCheck, clientIter)
				}

				for _, clientIter := range clientsForNewGameCheck {
					clientIter.mu.Lock()
					if clientIter.username != "" {
						totalIdentifiedCount++
						if !clientIter.isReady {
							allReadyForNewGame = false
						} else {
							identifiedAndReadyCount++
						}
					}
					clientIter.mu.Unlock()
				}

				// The minimum number of players to start could be 1 if Gandalf is there or 2 normally.
				// For now, if all the identified ones are ready and there is at least 1.
				canStart := totalIdentifiedCount > 0 && allReadyForNewGame && identifiedAndReadyCount == totalIdentifiedCount
				c.Hub.mu.Unlock() // Unlock hub here, after readings.

				if canStart {
					// Double check in case the game state changed between reads (even though gameState.mu is already free)
					log.Println("Todos los jugadores identificados están listos. Intentando iniciar nuevo juego.")
					c.Hub.startGame()
				} else {
					log.Printf("No todos listos para nuevo juego, o no suficientes jugadores. Listos: %d/%d.", identifiedAndReadyCount, totalIdentifiedCount)
				}
			} else {
				// Remaining cases:
				// 1. Game in progress, but the player is "NOT ready" (isNowReady == false).
				// 2. There is no game in progress, and the player has been set to "NOT ready".
				// In these cases, you won't be able to start or join a game, just update the user list.
				c.Hub.gameState.mu.Unlock()
				log.Printf("Jugador %s cambió su estado de listo a %t. Juego en progreso: %t. No se toman acciones de inicio/unión.", currentPlayerUsername, isNowReady, isGameCurrentlyInProgress)
			}

		case "requestStartGame": // Mensaje enviado por Gandalf para iniciar solo
			c.mu.Lock()
			isGandalf := c.username == "Gandalf"
			isGandalfReady := c.isReady
			c.mu.Unlock()

			if isGandalf && isGandalfReady {
				c.Hub.gameState.mu.Lock()
				gameWasInProgress := c.Hub.gameState.gameInProgress
				c.Hub.gameState.mu.Unlock()

				if !gameWasInProgress {
					c.Hub.mu.Lock()
					numIdentified := 0
					for cl := range c.Hub.clients {
						cl.mu.Lock()
						if cl.username != "" {
							numIdentified++
						}
						cl.mu.Unlock()
					}
					c.Hub.mu.Unlock()
					if numIdentified >= 1 { // Gandalf puede iniciar si hay al menos 1 (él mismo)
						log.Printf("Gandalf (listo) solicitó iniciar el juego. Jugadores identificados: %d", numIdentified)
						c.Hub.startGame()
					}
				} else {
					log.Printf("Gandalf intentó iniciar, pero un juego ya está en progreso.")
				}
			}
		case "playerLeaveGame": // When a player leaves the game
			log.Printf("Jugador %s salió de la partida (botón 'Salir de la Partida').", c.username)
			c.mu.Lock()
			c.isReady = false
			usernameLeaving := c.username
			c.mu.Unlock()

			c.Hub.broadcastUserList()

			c.Hub.gameState.mu.Lock()
			if c.Hub.gameState.gameInProgress {
				if c.Hub.gameState.currentDrawerUsername == usernameLeaving {
					log.Printf("El dibujante %s abandonó. Terminando turno.", usernameLeaving)
					if c.Hub.gameState.turnTimer != nil {
						c.Hub.gameState.turnTimer.Stop()
					}
					c.Hub.broadcastTurnOver("El dibujante abandonó la partida.", c.Hub.gameState.currentWord)
					c.Hub.assignNextDrawer()
				} else {
					// Si era un adivinador, quitarlo de la lista de los que adivinaron
					delete(c.Hub.gameState.playersWhoGuessedThisTurn, usernameLeaving)
					// Podríamos chequear si todos los restantes adivinaron
					c.Hub.checkIfAllGuessed()
				}
				// Quitarlo de playerOrder si estaba
				var newPlayerOrder []*Client
				foundAndRemoved := false
				for _, p := range c.Hub.gameState.playerOrder {
					if p != c { // Comparar punteros de cliente
						newPlayerOrder = append(newPlayerOrder, p)
					} else {
						foundAndRemoved = true
					}
				}
				if foundAndRemoved {
					c.Hub.gameState.playerOrder = newPlayerOrder
					log.Printf("Jugador %s removido de playerOrder.", usernameLeaving)
				}

				// Si playerOrder queda vacío y el juego estaba en curso, terminarlo.
				if len(c.Hub.gameState.playerOrder) == 0 && c.Hub.gameState.gameInProgress {
					log.Println("No quedan jugadores en la partida. Terminando juego.")
					c.Hub.gameState.gameInProgress = false // Asegurarse de cambiar el estado
					c.Hub.broadcastGameEnd("No quedan jugadores en la partida.")
				} else if len(c.Hub.gameState.playerOrder) > 0 && c.Hub.gameState.currentPlayerIndex >= len(c.Hub.gameState.playerOrder) {
					// Si el índice actual queda fuera de rango (por ejemplo, el último jugador de la lista se fue)
					// reiniciar el índice para que assignNextDrawer lo maneje correctamente.
					log.Println("Ajustando currentPlayerIndex después de que un jugador se fue.")
					c.Hub.gameState.currentPlayerIndex = -1 // Para que assignNextDrawer elija al siguiente correctamente
				}
			}
			c.Hub.gameState.mu.Unlock()

		case "wordChosenByDrawer":
			var payload WordChosenPayload
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				log.Printf("Error al decodificar WordChosenPayload: %v", err)
				continue
			}

			c.Hub.gameState.mu.Lock()
			// Make sure the sender is the current artist.
			if c.username == c.Hub.gameState.currentDrawerUsername {
				c.Hub.handleWordChosen(c.username, payload.ChosenWord)
			} else {
				log.Printf("Advertencia: %s intentó elegir palabra, pero el dibujante es %s", c.username, c.Hub.gameState.currentDrawerUsername)
			}
			c.Hub.gameState.mu.Unlock()
		case "drawAction":
			c.Hub.gameState.mu.Lock()
			// Only the current dibujante can submit drawing actions.
			if c.username == c.Hub.gameState.currentDrawerUsername && c.Hub.gameState.gameInProgress {
				rawDrawPayload := msg.Payload

				// Store the drawing action (its raw payload)
				c.Hub.gameState.currentTurnDrawActions = append(c.Hub.gameState.currentTurnDrawActions, rawDrawPayload)

				drawMsgForBroadcast := Message{Type: "drawAction", Payload: rawDrawPayload}
				drawMsgBytes, err := json.Marshal(drawMsgForBroadcast)
				if err != nil {
					log.Printf("Error al serializar mensaje drawAction final para broadcast: %v", err)
					c.Hub.gameState.mu.Unlock()
					continue
				}

				// Unlock gameState BEFORE transmitting to avoid holding the lock during network operations.
				c.Hub.gameState.mu.Unlock()

				// Broadcast to all clients (including the artist, so everyone sees the same thing)
				c.Hub.mu.Lock()
				for clientItem := range c.Hub.clients {
					select {
					case clientItem.Send <- drawMsgBytes:
					default:
						log.Printf("Canal de envío bloqueado para %s al retransmitir drawAction", clientItem.username)
					}
				}
				c.Hub.mu.Unlock()

			} else {
				log.Printf("Usuario %s intentó enviar drawAction pero no es el dibujante (%s) o juego no activo.", c.username, c.Hub.gameState.currentDrawerUsername)
				c.Hub.gameState.mu.Unlock()
			}

		default:
			log.Printf("Tipo de mensaje desconocido de %s: %s", c.username, msg.Type)
		}
	}
}

func (c *Client) WritePump() {
	defer func() {
		c.Conn.Close()
		log.Printf("Conexión cerrada para: %s (writePump)", c.username)
	}()
	for {
		select {
		case message, ok := <-c.Send:
			if !ok {
				// The hub closed the channel.
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("Error de escritura de WebSocket para %s: %v", c.username, err)
				return
			}
		}
	}
}
