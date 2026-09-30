package game_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/game"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
)

func TestActiveRoomMoveProcessing(t *testing.T) {
	gen := puzzle.NewGenerator()
	pz, _ := gen.Generate(puzzle.DifficultyEasy)

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
				ID:         teamID,
				MatchID:    matchID,
				TeamNumber: 1,
				Participants: []*match.MatchParticipant{
					{
						ID:       uuid.New(),
						MatchID:  matchID,
						TeamID:   teamID,
						PlayerID: &playerID,
						IsBot:    false,
						SlotNo:   1,
					},
				},
			},
		},
	}

	room := game.NewActiveRoom(m, nil)

	// Find an empty cell
	emptyCell := -1
	for i := 0; i < 81; i++ {
		if pz.Board[i] == 0 {
			emptyCell = i
			break
		}
	}

	correctVal := pz.Solution[emptyCell]
	pIDStr := playerID.String()

	// 1st correct move
	res, err := room.ProcessMove("req-1", pIDStr, emptyCell, correctVal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.IsCorrect || res.Points != 100 || res.NewCombo != 1 {
		t.Fatalf("expected 100 points, combo 1; got %d pts, combo %d", res.Points, res.NewCombo)
	}

	// Test duplicate request ID (Idempotency)
	_, err = room.ProcessMove("req-1", pIDStr, emptyCell, correctVal)
	if err == nil {
		t.Fatalf("expected error for duplicate request_id")
	}

	// Test move on already solved cell with new request_id
	_, err = room.ProcessMove("req-2", pIDStr, emptyCell, correctVal)
	if err == nil {
		t.Fatalf("expected error for already solved cell")
	}
}
