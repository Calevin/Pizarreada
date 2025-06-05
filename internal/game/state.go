package game

import (
	"encoding/json"
	"sync"
	"time"
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
	maxTurns                  int               // It will be calculated based on players and turnsPerPlayer
	currentTurnDrawActions    []json.RawMessage // Stores the payloads of the turn's drawAction messages
	mu                        sync.Mutex
}

func newGameState() *GameState {
	return &GameState{
		gameInProgress:            false,
		currentPlayerIndex:        -1, // No one is drawing initially
		scores:                    make(map[string]int),
		playerDrawCounts:          make(map[string]int),
		playersWhoGuessedThisTurn: make(map[string]bool),
		currentTurnDrawActions:    make([]json.RawMessage, 0),
	}
}
