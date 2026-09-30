package bot_test

import (
	"testing"
	"time"

	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/puzzle"
)

func TestBotMoveDelays(t *testing.T) {
	// Table test against the specification matrix
	// bot|easy|medium|hard|expert
	// noob|12|15|18|22
	// pro|10|12|15|18
	// expert|8|11|12|16
	// devil|4|6|7|10

	tests := []struct {
		botDiff    bot.Difficulty
		puzDiff    puzzle.Difficulty
		expectedSec int
	}{
		{bot.DifficultyNoob, puzzle.DifficultyEasy, 12},
		{bot.DifficultyNoob, puzzle.DifficultyMedium, 15},
		{bot.DifficultyNoob, puzzle.DifficultyHard, 18},
		{bot.DifficultyNoob, puzzle.DifficultyExpert, 22},

		{bot.DifficultyPro, puzzle.DifficultyEasy, 10},
		{bot.DifficultyPro, puzzle.DifficultyMedium, 12},
		{bot.DifficultyPro, puzzle.DifficultyHard, 15},
		{bot.DifficultyPro, puzzle.DifficultyExpert, 18},

		{bot.DifficultyExpert, puzzle.DifficultyEasy, 8},
		{bot.DifficultyExpert, puzzle.DifficultyMedium, 11},
		{bot.DifficultyExpert, puzzle.DifficultyHard, 12},
		{bot.DifficultyExpert, puzzle.DifficultyExpert, 16},

		{bot.DifficultyDevil, puzzle.DifficultyEasy, 4},
		{bot.DifficultyDevil, puzzle.DifficultyMedium, 6},
		{bot.DifficultyDevil, puzzle.DifficultyHard, 7},
		{bot.DifficultyDevil, puzzle.DifficultyExpert, 10},
	}

	for _, tt := range tests {
		delay := bot.GetMoveDelay(tt.botDiff, tt.puzDiff)
		if delay != time.Duration(tt.expectedSec)*time.Second {
			t.Errorf("expected %ds delay for bot %s on puzzle %s, got %v", tt.expectedSec, tt.botDiff, tt.puzDiff, delay)
		}
	}
}

func TestBotDecideMove(t *testing.T) {
	b := bot.NewBot(bot.DifficultyDevil) // 100% accuracy
	gen := puzzle.NewGenerator()
	p, _ := gen.Generate(puzzle.DifficultyEasy)

	cell, val, unsolvable := b.DecideMove(p.Board, p.Solution)
	if unsolvable {
		t.Fatalf("expected solvable board")
	}

	if p.Solution[cell] != val {
		t.Fatalf("devil bot with 100%% accuracy should make correct move, expected %d got %d", p.Solution[cell], val)
	}
}
