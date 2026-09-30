package game_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/game"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
)

func newTestMatch(t *testing.T) (*match.Match, string) {
	t.Helper()
	gen := puzzle.NewGenerator()
	pz, err := gen.Generate(puzzle.DifficultyEasy)
	if err != nil {
		t.Fatalf("failed to generate puzzle: %v", err)
	}

	playerID := uuid.New()
	teamID := uuid.New()
	matchID := uuid.New()

	m := &match.Match{
		ID:          matchID,
		Status:      match.StatusPlaying,
		Difficulty:  puzzle.DifficultyEasy,
		PuzzleBoard: pz.Board,
		Solution:    pz.Solution,
		BoardState:  pz.Board,
		Teams: []*match.MatchTeam{
			{
				ID:           teamID,
				MatchID:      matchID,
				TeamNumber:   1,
				Participants: []*match.MatchParticipant{{ID: uuid.New(), MatchID: matchID, TeamID: teamID, PlayerID: &playerID, SlotNo: 1}},
			},
		},
	}
	return m, playerID.String()
}

func findEmptyCell(pz [81]int) int {
	for i := 0; i < 81; i++ {
		if pz[i] == 0 {
			return i
		}
	}
	return -1
}

func TestMoveThrottling(t *testing.T) {
	m, pID := newTestMatch(t)
	cfg := &config.Config{MoveMinInterval: time.Hour} // effectively blocks 2nd move
	room := game.NewActiveRoom(m, nil)
	room.Cfg = cfg

	cell := findEmptyCell(m.BoardState)
	res, err := room.ProcessMove("req-1", pID, cell, m.Solution[cell])
	if err != nil {
		t.Fatalf("first move failed: %v", err)
	}
	if !res.IsCorrect {
		t.Fatalf("expected correct move")
	}

	_, err = room.ProcessMove("req-2", pID, findEmptyCell(m.BoardState), m.Solution[findEmptyCell(m.BoardState)])
	if err == nil {
		t.Fatalf("expected throttling error for rapid second move")
	}
}

func TestWrongMovePenaltyAndCooldown(t *testing.T) {
	m, pID := newTestMatch(t)
	cfg := &config.Config{MoveMinInterval: 0, WrongMoveCooldown: time.Hour, WrongMovePenalty: 50}
	room := game.NewActiveRoom(m, nil)
	room.Cfg = cfg

	cell := findEmptyCell(m.BoardState)
	wrongVal := (m.Solution[cell] % 9) + 1

	res, err := room.ProcessMove("req-1", pID, cell, wrongVal)
	if err != nil {
		t.Fatalf("wrong move should be accepted and penalized: %v", err)
	}
	if res.IsCorrect || res.Points != 0 || res.Penalty != 50 {
		t.Fatalf("expected wrong move with 50 penalty, got %+v", res)
	}
	if res.TotalScore != 0 {
		t.Fatalf("score should floor at 0, got %d", res.TotalScore)
	}

	// Next move must be blocked by the wrong-answer cooldown.
	_, err = room.ProcessMove("req-2", pID, findEmptyCell(m.BoardState), m.Solution[findEmptyCell(m.BoardState)])
	if err == nil {
		t.Fatalf("expected cooldown error after wrong move")
	}
}

func TestDuplicateRequestIDRejected(t *testing.T) {
	m, pID := newTestMatch(t)
	room := game.NewActiveRoom(m, nil)

	cell := findEmptyCell(m.BoardState)
	if _, err := room.ProcessMove("req-1", pID, cell, m.Solution[cell]); err != nil {
		t.Fatalf("first move failed: %v", err)
	}
	if _, err := room.ProcessMove("req-1", pID, cell, m.Solution[cell]); err == nil {
		t.Fatalf("expected duplicate request_id error")
	}
}

func TestFinishReleasesLocksAndPublishesEvent(t *testing.T) {
	m, pID := newTestMatch(t)
	room := game.NewActiveRoom(m, nil)
	room.Cfg = &config.Config{MoveMinInterval: 1, WrongMoveCooldown: 1} // 1ns = no throttle

	events := make(chan string, 128)
	room.OnStateChange = func(rm *game.ActiveRoom, eventType string, payload interface{}) {
		events <- eventType
	}

	// Solve the whole board as the single player.
	board := m.BoardState
	for i := 0; i < 81; i++ {
		if board[i] == 0 {
			if _, err := room.ProcessMove(reqID(i), pID, i, m.Solution[i]); err != nil {
				t.Fatalf("move %d failed: %v", i, err)
			}
		}
	}

	if !room.IsFinished() {
		t.Fatalf("expected match to be finished after solving the board")
	}

	// game.finished is broadcast asynchronously by postFinalize.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case et := <-events:
			if et == "game.finished" {
				return
			}
		case <-deadline:
			t.Fatalf("expected game.finished broadcast within 2s")
		}
	}
}

func reqID(i int) string { return "req-" + uuid.New().String() + "-" + string(rune('a'+i%26)) }
