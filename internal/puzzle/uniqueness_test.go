package puzzle_test

import (
	"testing"

	"github.com/h3x02/suduxapi/internal/puzzle"
)

// TestPuzzleSolutionsAreUnique verifies the generator never returns a puzzle
// with more than one solution. Multiple solutions would make ValidateMove
// reject correct plays, since the server compares against a single stored
// solution.
func TestPuzzleSolutionsAreUnique(t *testing.T) {
	gen := puzzle.NewGenerator()

	for _, diff := range []puzzle.Difficulty{
		puzzle.DifficultyEasy,
		puzzle.DifficultyMedium,
		puzzle.DifficultyHard,
		puzzle.DifficultyExpert,
	} {
		t.Run(string(diff), func(t *testing.T) {
			p, err := gen.Generate(diff)
			if err != nil {
				t.Fatalf("failed to generate puzzle: %v", err)
			}

			board := p.Board
			if solutions := gen.CountSolutionsForTest(&board, 2); solutions != 1 {
				t.Fatalf("expected exactly 1 solution, got %d", solutions)
			}
		})
	}
}
