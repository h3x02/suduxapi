package puzzle_test

import (
	"testing"

	"github.com/h3x02/suduxapi/internal/puzzle"
)

func TestPuzzleGenerator(t *testing.T) {
	gen := puzzle.NewGenerator()
	p, err := gen.Generate(puzzle.DifficultyEasy)
	if err != nil {
		t.Fatalf("failed to generate puzzle: %v", err)
	}

	if p.Difficulty != puzzle.DifficultyEasy {
		t.Fatalf("expected easy difficulty, got %s", p.Difficulty)
	}

	// Verify solution grid is fully filled (1-9)
	for i, val := range p.Solution {
		if val < 1 || val > 9 {
			t.Fatalf("invalid value in solution at index %d: %d", i, val)
		}
	}

	// Check clue count
	clueCount := 0
	for _, val := range p.Board {
		if val != 0 {
			clueCount++
		}
	}
	if clueCount != 42 {
		t.Fatalf("expected 42 clues for easy difficulty, got %d", clueCount)
	}
}

func TestValidateMove(t *testing.T) {
	gen := puzzle.NewGenerator()
	p, _ := gen.Generate(puzzle.DifficultyMedium)

	validCell := 0
	correctVal := p.Solution[validCell]
	wrongVal := (correctVal % 9) + 1

	if !puzzle.ValidateMove(&p.Solution, validCell, correctVal) {
		t.Fatalf("expected correct move to validate true")
	}

	if puzzle.ValidateMove(&p.Solution, validCell, wrongVal) {
		t.Fatalf("expected wrong move to validate false")
	}

	if puzzle.ValidateMove(&p.Solution, -1, correctVal) {
		t.Fatalf("expected out-of-bounds cell to validate false")
	}
}

func TestCellRaceScoring(t *testing.T) {
	scoring := puzzle.NewCellRaceScoring()

	// 1st correct move
	pts, combo := scoring.CalculateScore(0, true)
	if pts != 100 || combo != 1 {
		t.Fatalf("expected 100 pts, combo 1; got %d pts, combo %d", pts, combo)
	}

	// 2nd correct move
	pts, combo = scoring.CalculateScore(combo, true)
	if pts != 110 || combo != 2 {
		t.Fatalf("expected 110 pts, combo 2; got %d pts, combo %d", pts, combo)
	}

	// 3rd correct move
	pts, combo = scoring.CalculateScore(combo, true)
	if pts != 120 || combo != 3 {
		t.Fatalf("expected 120 pts, combo 3; got %d pts, combo %d", pts, combo)
	}

	// Incorrect move resets combo
	pts, combo = scoring.CalculateScore(combo, false)
	if pts != 0 || combo != 0 {
		t.Fatalf("expected 0 pts, combo 0; got %d pts, combo %d", pts, combo)
	}
}
