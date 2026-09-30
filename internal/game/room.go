package game

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
)

type RoomParticipantState struct {
	PlayerID    string `json:"player_id"`
	TeamID      string `json:"team_id"`
	IsBot       bool   `json:"is_bot"`
	Score       int    `json:"score"`
	Combo       int    `json:"combo"`
	SolvedCells int    `json:"solved_cells"`
	Connected   bool   `json:"connected"`
}

type ActiveRoom struct {
	mu            sync.Mutex
	MatchID       uuid.UUID
	Match         *match.Match
	BoardState    [81]int
	Solution      [81]int
	CellOwners    [81]string // player_id or bot_id
	Participants  map[string]*RoomParticipantState
	Scoring       puzzle.ScoringEngine
	ProcessedReqs map[string]bool // idempotency key -> bool
	MatchRepo     *match.Repository
	OnStateChange func(room *ActiveRoom, eventType string, payload interface{})
	CancelBot     context.CancelFunc
}

func NewActiveRoom(m *match.Match, repo *match.Repository) *ActiveRoom {
	room := &ActiveRoom{
		MatchID:       m.ID,
		Match:         m,
		BoardState:    m.BoardState,
		Solution:      m.Solution,
		Participants:  make(map[string]*RoomParticipantState),
		Scoring:       puzzle.NewCellRaceScoring(),
		ProcessedReqs: make(map[string]bool),
		MatchRepo:     repo,
	}

	for _, t := range m.Teams {
		for _, p := range t.Participants {
			pID := "bot"
			if p.PlayerID != nil {
				pID = p.PlayerID.String()
			} else if p.IsBot {
				pID = fmt.Sprintf("bot_%s", t.ID.String())
			}
			room.Participants[pID] = &RoomParticipantState{
				PlayerID:    pID,
				TeamID:      t.ID.String(),
				IsBot:       p.IsBot,
				Score:       p.Score,
				Combo:       p.Combo,
				SolvedCells: p.SolvedCells,
				Connected:   p.IsBot, // bots are always connected
			}
		}
	}

	return room
}

type MoveResult struct {
	Cell       int    `json:"cell"`
	Value      int    `json:"value"`
	IsCorrect  bool   `json:"is_correct"`
	PlayerID   string `json:"player_id"`
	TeamID     string `json:"team_id"`
	Points     int    `json:"points"`
	NewCombo   int    `json:"new_combo"`
	TotalScore int    `json:"total_score"`
}

func (r *ActiveRoom) ProcessMove(reqID, playerID string, cell, val int) (*MoveResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if reqID != "" {
		if r.ProcessedReqs[reqID] {
			return nil, fmt.Errorf("duplicate request_id: move already processed")
		}
		r.ProcessedReqs[reqID] = true
	}

	if r.Match.Status != match.StatusPlaying {
		return nil, fmt.Errorf("match is not active")
	}

	pState, ok := r.Participants[playerID]
	if !ok {
		return nil, fmt.Errorf("player is not participant in match")
	}

	if cell < 0 || cell >= 81 {
		return nil, fmt.Errorf("cell out of range")
	}

	// Cell already solved?
	if r.BoardState[cell] != 0 {
		return nil, fmt.Errorf("cell already solved")
	}

	isCorrect := puzzle.ValidateMove(&r.Solution, cell, val)
	points, newCombo := r.Scoring.CalculateScore(pState.Combo, isCorrect)

	pState.Combo = newCombo
	if isCorrect {
		r.BoardState[cell] = val
		r.CellOwners[cell] = playerID
		pState.Score += points
		pState.SolvedCells++

		// Update team score
		for _, t := range r.Match.Teams {
			if t.ID.String() == pState.TeamID {
				t.Score += points
				break
			}
		}
	}

	res := &MoveResult{
		Cell:       cell,
		Value:      val,
		IsCorrect:  isCorrect,
		PlayerID:   playerID,
		TeamID:     pState.TeamID,
		Points:     points,
		NewCombo:   newCombo,
		TotalScore: pState.Score,
	}

	// Check if puzzle is fully solved
	if isCorrect && r.isPuzzleComplete() {
		r.finishMatchLocked()
	}

	return res, nil
}

func (r *ActiveRoom) isPuzzleComplete() bool {
	for i := 0; i < 81; i++ {
		if r.BoardState[i] == 0 {
			return false
		}
	}
	return true
}

func (r *ActiveRoom) finishMatchLocked() {
	r.Match.Status = match.StatusFinished
	if r.CancelBot != nil {
		r.CancelBot()
	}

	// Determine winning team
	var highestScore = -1
	var winningTeamID *uuid.UUID

	for _, t := range r.Match.Teams {
		if t.Score > highestScore {
			highestScore = t.Score
			id := t.ID
			winningTeamID = &id
		}
	}

	r.Match.WinningTeamID = winningTeamID

	for _, t := range r.Match.Teams {
		for _, p := range t.Participants {
			pID := "bot"
			if p.PlayerID != nil {
				pID = p.PlayerID.String()
			} else if p.IsBot {
				pID = fmt.Sprintf("bot_%s", t.ID.String())
			}
			pState := r.Participants[pID]
			p.Score = pState.Score
			p.SolvedCells = pState.SolvedCells

			resStr := "loss"
			if winningTeamID != nil && t.ID == *winningTeamID {
				resStr = "win"
			}
			p.Result = &resStr
		}
	}

	r.Match.BoardState = r.BoardState
	if r.MatchRepo != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = r.MatchRepo.SaveMatchResult(ctx, r.Match)
		}()
	}
}

func (r *ActiveRoom) StartBotRunner(ctx context.Context) {
	var botTeam *match.MatchTeam
	for _, t := range r.Match.Teams {
		if t.IsBotTeam {
			botTeam = t;
			break
		}
	}
	if botTeam == nil || botTeam.BotDifficulty == nil {
		return
	}

	botID := fmt.Sprintf("bot_%s", botTeam.ID.String())
	botInstance := bot.NewBot(*botTeam.BotDifficulty)
	delay := bot.GetMoveDelay(*botTeam.BotDifficulty, r.Match.Difficulty)

	botCtx, cancel := context.WithCancel(ctx)
	r.CancelBot = cancel

	go func() {
		ticker := time.NewTicker(delay)
		defer ticker.Stop()

		for {
			select {
			case <-botCtx.Done():
				return
			case <-ticker.C:
				r.mu.Lock()
				if r.Match.Status != match.StatusPlaying {
					r.mu.Unlock()
					return
				}
				cell, val, unsolvable := botInstance.DecideMove(r.BoardState, r.Solution)
				r.mu.Unlock()

				if unsolvable {
					return
				}

				reqID := fmt.Sprintf("bot_move_%d", time.Now().UnixNano())
				res, err := r.ProcessMove(reqID, botID, cell, val)
				if err == nil && r.OnStateChange != nil {
					r.OnStateChange(r, "game.move_result", res)
				}
			}
		}
	}()
}
