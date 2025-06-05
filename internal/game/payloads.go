package game

// UserListPayload it is used to send the list of connected users.
type UserListPayload struct {
	Users []UserDetails `json:"users"`
}

// UserDetails contains information about a logged in user.
type UserDetails struct {
	Username string `json:"username"`
	IsReady  bool   `json:"isReady"`
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

// AssignDrawerPayload to send words to the dibujante.
type AssignDrawerPayload struct {
	DrawerUsername      string   `json:"drawerUsername"`
	WordsToChoose       []string `json:"wordsToChoose,omitempty"` // Only for the dibujante
	Duration            int      `json:"duration,omitempty"`      // Time to choose a word
	TotalTurnsCompleted int      `json:"totalTurnsCompleted"`
	MaxTurns            int      `json:"maxTurns"`
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

// GuessCorrectPayload to notify a correct guess.
type GuessCorrectPayload struct {
	GuesserUsername string `json:"guesserUsername"`
	PointsGuesser   int    `json:"pointsGuesser"`
	PointsDrawer    int    `json:"pointsDrawer"`           // Points the dibujante earns for this riddle
	IsTurnOver      bool   `json:"isTurnOver"`             // If this was the last one to guess
	WordRevealed    string `json:"wordRevealed,omitempty"` // The word, if the turn is over
}
