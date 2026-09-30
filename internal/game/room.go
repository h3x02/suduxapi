package game

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/activematch"
	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/redis/go-redis/v9"
)

var (
	ErrMatchNotActive     = errors.New("match is not active")
	ErrNotParticipant     = errors.New("player is not participant in match")
	ErrCellOutOfRange     = errors.New("cell out of range")
	ErrCellAlreadySolved  = errors.New("cell already solved")
	ErrDuplicateRequest   = errors.New("duplicate request_id: move already processed")
	ErrMovingTooFast      = errors.New("moving too fast, slow down")
	ErrWrongMoveCooldown  = errors.New("wrong answer cooldown active, wait before next move")
	ErrNotConnected       = errors.New("player is not connected to this match")
)

// maxProcessedReqs caps the idempotency map so it cannot grow unbounded on a
// long-running room. Once full, oldest entries are dropped in bulk; the cap is
// far above any realistic client duplicate window.
const maxProcessedReqs = 10000

// progressSaveInterval controls how often live board state is persisted so a
// game-server restart doesn't wipe players' progress.
const progressSaveInterval = 15 * time.Second

type RoomParticipantState struct {
	PlayerID      string    `json:"player_id"`
	TeamID        string    `json:"team_id"`
	IsBot         bool      `json:"is_bot"`
	Score         int       `json:"score"`
	Combo         int       `json:"combo"`
	SolvedCells   int       `json:"solved_cells"`
	Connected     bool      `json:"connected"`
	LastMoveAt    time.Time `json:"-"`
	CooldownUntil time.Time `json:"-"`
}

type RoomSnapshot struct {
	MatchID      string                           `json:"match_id"`
	Status       match.Status                     `json:"status"`
	BoardState   [81]int                          `json:"board_state"`
	CellOwners   [81]string                       `json:"cell_owners"`
	Participants map[string]*RoomParticipantState `json:"participants"`
	Teams        []*match.MatchTeam               `json:"teams"`
	DeadlineAt   *time.Time                       `json:"deadline_at,omitempty"`
}

type ActiveRoom struct {
	mu            sync.Mutex
	MatchID       uuid.UUID
	Match         *match.Match
	BoardState    [81]int
	Solution      [81]int
	CellOwners    [81]string
	Participants  map[string]*RoomParticipantState
	Scoring       puzzle.ScoringEngine
	ProcessedReqs map[string]struct{}
	reqOrder      []string // FIFO of request IDs for eviction
	MatchRepo     *match.Repository
	Redis         *redis.Client
	Cfg           *config.Config
	OnStateChange func(room *ActiveRoom, eventType string, payload interface{})
	CancelBot     context.CancelFunc

	winningTeamID *uuid.UUID
	finalizeOnce  sync.Once

	lastProgressSave time.Time
	MatchFinished    bool
}

func NewActiveRoom(m *match.Match, repo *match.Repository) *ActiveRoom {
	room := &ActiveRoom{
		MatchID:       m.ID,
		Match:         m,
		BoardState:    m.BoardState,
		Solution:      m.Solution,
		Participants:  make(map[string]*RoomParticipantState),
		Scoring:       puzzle.NewCellRaceScoring(),
		ProcessedReqs: make(map[string]struct{}),
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

// Snapshot returns a consistent copy of the room state. Hub and broadcasts must
// use this instead of reading fields concurrently with ProcessMove.
func (r *ActiveRoom) Snapshot() RoomSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()

	participants := make(map[string]*RoomParticipantState, len(r.Participants))
	for k, v := range r.Participants {
		cp := *v
		participants[k] = &cp
	}
	return RoomSnapshot{
		MatchID:      r.MatchID.String(),
		Status:       r.Match.Status,
		BoardState:   r.BoardState,
		CellOwners:   r.CellOwners,
		Participants: participants,
		Teams:        r.Match.Teams,
		DeadlineAt:   r.Match.DeadlineAt,
	}
}

// SetConnected marks a participant's connection state. Takes the room lock so
// it cannot race with move processing.
func (r *ActiveRoom) SetConnected(playerID string, connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.Participants[playerID]; ok {
		p.Connected = connected
	}
}

// HasHumanConnected reports whether any human participant is connected.
func (r *ActiveRoom) HasHumanConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.Participants {
		if !p.IsBot && p.Connected {
			return true
		}
	}
	return false
}

// HumanParticipantIDs returns the IDs of all non-bot participants.
func (r *ActiveRoom) HumanParticipantIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var ids []string
	for _, p := range r.Participants {
		if !p.IsBot {
			ids = append(ids, p.PlayerID)
		}
	}
	return ids
}

func (r *ActiveRoom) ProcessMove(reqID, playerID string, cell, val int) (*MoveResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.Match.Status != match.StatusPlaying {
		return nil, ErrMatchNotActive
	}

	pState, ok := r.Participants[playerID]
	if !ok {
		return nil, ErrNotParticipant
	}

	// Idempotency: mark the request as seen only once. If validation later
	// fails the request stays marked, which is correct: a replay of the same
	// request_id must never be processed twice.
	if reqID != "" {
		if _, dup := r.ProcessedReqs[reqID]; dup {
			return nil, ErrDuplicateRequest
		}
		r.ProcessedReqs[reqID] = struct{}{}
		r.reqOrder = append(r.reqOrder, reqID)
		if len(r.reqOrder) > maxProcessedReqs {
			// Evict the oldest half in bulk.
			evict := r.reqOrder[:maxProcessedReqs/2]
			for _, old := range evict {
				delete(r.ProcessedReqs, old)
			}
			r.reqOrder = append([]string{}, r.reqOrder[maxProcessedReqs/2:]...)
		}
	}

	if cell < 0 || cell >= 81 {
		return nil, ErrCellOutOfRange
	}
	if val < 1 || val > 9 {
		return nil, fmt.Errorf("value out of range")
	}

	// Anti brute-force throttling: minimum interval between moves, plus a
	// longer cooldown after a wrong answer. Without this a client could
	// enumerate the 9 possible values per cell at network speed.
	now := time.Now()
	if !pState.IsBot {
		if now.Before(pState.CooldownUntil) {
			return nil, ErrWrongMoveCooldown
		}
		if !pState.LastMoveAt.IsZero() && now.Sub(pState.LastMoveAt) < r.moveMinInterval() {
			return nil, ErrMovingTooFast
		}
	}
	pState.LastMoveAt = now

	// Cell already solved?
	if r.BoardState[cell] != 0 {
		return nil, ErrCellAlreadySolved
	}

	isCorrect := puzzle.ValidateMove(&r.Solution, cell, val)
	points, newCombo := r.Scoring.CalculateScore(pState.Combo, isCorrect)

	pState.Combo = newCombo
	if !isCorrect {
		// Wrong move: penalize and cool down instead of letting combos-only
		// punishment make guessing free.
		penalty := r.wrongMovePenalty()
		if penalty > 0 {
			pState.Score -= penalty
			if pState.Score < 0 {
				pState.Score = 0
			}
			for _, t := range r.Match.Teams {
				if t.ID.String() == pState.TeamID {
					t.Score -= penalty
					if t.Score < 0 {
						t.Score = 0
					}
					break
				}
			}
		}
		pState.CooldownUntil = now.Add(r.wrongMoveCooldown())
	} else {
		r.BoardState[cell] = val
		r.CellOwners[cell] = playerID
		pState.Score += points
		pState.SolvedCells++

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
		Penalty:    r.wrongMovePenaltyIf(!isCorrect),
		NewCombo:   newCombo,
		TotalScore: pState.Score,
	}

	r.maybePersistProgressLocked(now)

	if isCorrect && r.isPuzzleComplete() {
		// Finalize atomically with the move (still holding r.mu) so no other
		// move can interleave. Post-processing (DB save, lock release, event
		// broadcast) happens lock-free in finalize.
		r.markFinishedLocked("puzzle_complete", r.highestScoreTeamIDLocked())
		r.postFinalize("puzzle_complete", r.winningTeamID)
	}

	return res, nil
}

func (r *ActiveRoom) moveMinInterval() time.Duration {
	if r.Cfg != nil && r.Cfg.MoveMinInterval > 0 {
		return r.Cfg.MoveMinInterval
	}
	return 250 * time.Millisecond
}

func (r *ActiveRoom) wrongMoveCooldown() time.Duration {
	if r.Cfg != nil && r.Cfg.WrongMoveCooldown > 0 {
		return r.Cfg.WrongMoveCooldown
	}
	return 2 * time.Second
}

func (r *ActiveRoom) wrongMovePenalty() int {
	if r.Cfg != nil {
		return r.Cfg.WrongMovePenalty
	}
	return 50
}

func (r *ActiveRoom) wrongMovePenaltyIf(wrong bool) int {
	if wrong {
		return r.wrongMovePenalty()
	}
	return 0
}

// maybePersistProgressLocked saves board state to Postgres at most once per
// progressSaveInterval while holding the room lock (serialization is fine: the
// DB write is cheap and infrequent).
func (r *ActiveRoom) maybePersistProgressLocked(now time.Time) {
	if r.MatchRepo == nil || now.Sub(r.lastProgressSave) < progressSaveInterval {
		return
	}
	r.lastProgressSave = now
	matchID := r.MatchID
	board := r.BoardState
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := r.MatchRepo.SaveMatchProgress(ctx, matchID, board); err != nil {
			logger.Log.Warn("failed to persist match progress", "match_id", matchID, "err", err)
		}
	}()
}

func (r *ActiveRoom) isPuzzleComplete() bool {
	for i := 0; i < 81; i++ {
		if r.BoardState[i] == 0 {
			return false
		}
	}
	return true
}

// IsFinished reports whether this room's match has ended (finished or
// cancelled). Used by the hub janitor to start the eviction clock for rooms
// that ended via ProcessMove rather than the janitor itself.
func (r *ActiveRoom) IsFinished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.MatchFinished || r.Match.Status == match.StatusFinished || r.Match.Status == match.StatusCancelled
}

// DeadlinePassed reports whether the server-side match deadline has expired.
func (r *ActiveRoom) DeadlinePassed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Match.DeadlineAt != nil && time.Now().After(*r.Match.DeadlineAt)
}// markFinishedLocked transitions the room to finished while r.mu is held.
// It syncs scores/results into the match struct and records the winner.
// The caller must invoke postFinalize afterwards, without the lock held.
func (r *ActiveRoom) markFinishedLocked(reason string, winningTeamID *uuid.UUID) {
	if r.MatchFinished {
		return
	}
	r.MatchFinished = true
	r.winningTeamID = winningTeamID
	r.Match.Status = match.StatusFinished
	r.Match.WinningTeamID = winningTeamID
	if r.CancelBot != nil {
		r.CancelBot()
	}

	for _, t := range r.Match.Teams {
		for _, p := range t.Participants {
			pID := "bot"
			if p.PlayerID != nil {
				pID = p.PlayerID.String()
			} else if p.IsBot {
				pID = fmt.Sprintf("bot_%s", t.ID.String())
			}
			pState := r.Participants[pID]
			if pState == nil {
				continue
			}
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
	_ = reason // reason is used by postFinalize logging/broadcast
}

// postFinalize performs the side effects of ending a match: active match-lock
// release, best-effort persisted result with retries, and the game.finished
// broadcast. Runs asynchronously in a single goroutine (guarded by finalizeOnce)
// because callers like ProcessMove invoke it while still holding r.mu — doing
// this work synchronously under the lock would deadlock.
func (r *ActiveRoom) postFinalize(reason string, winningTeamID *uuid.UUID) {
	r.finalizeOnce.Do(func() {
		go func() {
			r.mu.Lock()
			humanIDs := make([]string, 0, len(r.Participants))
			for _, p := range r.Participants {
				if !p.IsBot {
					humanIDs = append(humanIDs, p.PlayerID)
				}
			}
			matchID := r.MatchID
			finalMatch := r.Match
			repo := r.MatchRepo
			r.mu.Unlock()

			// Release active-match locks so players can queue again even if
			// the DB save below fails.
			if r.Redis != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				for _, pID := range humanIDs {
					if err := activematch.Unlock(ctx, r.Redis, pID); err != nil {
						logger.Log.Warn("failed to release active match lock", "player_id", pID, "err", err)
					}
				}
				cancel()
			}

			if repo != nil {
				var lastErr error
				for attempt := 1; attempt <= 3; attempt++ {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					err := repo.SaveMatchResult(ctx, finalMatch)
					cancel()
					if err == nil {
						lastErr = nil
						break
					}
					lastErr = err
					time.Sleep(time.Duration(attempt) * time.Second)
				}
				if lastErr != nil {
					logger.Log.Error("failed to save match result after retries", "match_id", matchID, "reason", reason, "err", lastErr)
				}
			}

			// Tell connected clients the match is over and who won.
			if r.OnStateChange != nil {
				r.OnStateChange(r, "game.finished", map[string]interface{}{
					"match_id": matchID.String(),
					"reason":   reason,
					"winners":  winningTeamID,
					"state":    r.Snapshot(),
				})
			}
		}()
	})
}

func (r *ActiveRoom) highestScoreTeamIDLocked() *uuid.UUID {
	var best *uuid.UUID
	highest := -1
	for _, t := range r.Match.Teams {
		if t.Score > highest {
			highest = t.Score
			id := t.ID
			best = &id
		}
	}
	return best
}

// FinishByTimeout ends the match when the deadline passes. Called by the hub
// janitor.
func (r *ActiveRoom) FinishByTimeout() {
	r.mu.Lock()
	alreadyDone := r.MatchFinished
	if !alreadyDone {
		r.markFinishedLocked("deadline_exceeded", r.highestScoreTeamIDLocked())
	}
	winner := r.winningTeamID
	r.mu.Unlock()
	if !alreadyDone {
		r.postFinalize("deadline_exceeded", winner)
	}
}

// FinishAbandoned ends a match where all humans left before completion.
func (r *ActiveRoom) FinishAbandoned() {
	r.mu.Lock()
	alreadyDone := r.MatchFinished
	if !alreadyDone {
		r.markFinishedLocked("abandoned", r.highestScoreTeamIDLocked())
	}
	winner := r.winningTeamID
	r.mu.Unlock()
	if !alreadyDone {
		r.postFinalize("abandoned", winner)
	}
}

// CancelMatch marks the match cancelled (used when the deadline janitor beats
// us to it in the DB). Locks are still released defensively.
func (r *ActiveRoom) CancelMatch() {
	r.mu.Lock()
	if r.MatchFinished {
		r.mu.Unlock()
		return
	}
	r.MatchFinished = true
	r.Match.Status = match.StatusCancelled
	if r.CancelBot != nil {
		r.CancelBot()
	}
	humanIDs := make([]string, 0, len(r.Participants))
	for _, p := range r.Participants {
		if !p.IsBot {
			humanIDs = append(humanIDs, p.PlayerID)
		}
	}
	r.mu.Unlock()

	if r.Redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, pID := range humanIDs {
			_ = activematch.Unlock(ctx, r.Redis, pID)
		}
	}
}

type MoveResult struct {
	Cell       int    `json:"cell"`
	Value      int    `json:"value"`
	IsCorrect  bool   `json:"is_correct"`
	PlayerID   string `json:"player_id"`
	TeamID     string `json:"team_id"`
	Points     int    `json:"points"`
	Penalty    int    `json:"penalty,omitempty"`
	NewCombo   int    `json:"new_combo"`
	TotalScore int    `json:"total_score"`
}

func (r *ActiveRoom) StartBotRunner(ctx context.Context) {
	var botTeam *match.MatchTeam
	for _, t := range r.Match.Teams {
		if t.IsBotTeam {
			botTeam = t
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
