package bot

import (
	"math/rand"
	"time"

	"github.com/h3x02/suduxapi/internal/puzzle"
)

type Difficulty string

const (
	DifficultyNoob   Difficulty = "noob"
	DifficultyPro    Difficulty = "pro"
	DifficultyExpert Difficulty = "expert"
	DifficultyDevil  Difficulty = "devil"
)

type Bot struct {
	Difficulty Difficulty
	Accuracy   float64 // 0.0 to 1.0
	rng        *rand.Rand
}

func NewBot(diff Difficulty) *Bot {
	var acc float64
	switch diff {
	case DifficultyNoob:
		acc = 0.70
	case DifficultyPro:
		acc = 0.80
	case DifficultyExpert:
		acc = 0.90
	case DifficultyDevil:
		acc = 1.00
	default:
		acc = 0.70
	}

	return &Bot{
		Difficulty: diff,
		Accuracy:   acc,
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// GetMoveDelay returns the delay for a bot based on bot difficulty and puzzle difficulty
// bot|easy|medium|hard|expert
// noob|12|15|18|22
// pro|10|12|15|18
// expert|8|11|12|16
// devil|4|6|7|10
func GetMoveDelay(botDiff Difficulty, puzzleDiff puzzle.Difficulty) time.Duration {
	var seconds int

	switch botDiff {
	case DifficultyNoob:
		switch puzzleDiff {
		case puzzle.DifficultyEasy:
			seconds = 12
		case puzzle.DifficultyMedium:
			seconds = 15
		case puzzle.DifficultyHard:
			seconds = 18
		case puzzle.DifficultyExpert:
			seconds = 22
		default:
			seconds = 15
		}
	case DifficultyPro:
		switch puzzleDiff {
		case puzzle.DifficultyEasy:
			seconds = 10
		case puzzle.DifficultyMedium:
			seconds = 12
		case puzzle.DifficultyHard:
			seconds = 15
		case puzzle.DifficultyExpert:
			seconds = 18
		default:
			seconds = 12
		}
	case DifficultyExpert:
		switch puzzleDiff {
		case puzzle.DifficultyEasy:
			seconds = 8
		case puzzle.DifficultyMedium:
			seconds = 11
		case puzzle.DifficultyHard:
			seconds = 12
		case puzzle.DifficultyExpert:
			seconds = 16
		default:
			seconds = 11
		}
	case DifficultyDevil:
		switch puzzleDiff {
		case puzzle.DifficultyEasy:
			seconds = 4
		case puzzle.DifficultyMedium:
			seconds = 6
		case puzzle.DifficultyHard:
			seconds = 7
		case puzzle.DifficultyExpert:
			seconds = 10
		default:
			seconds = 6
		}
	default:
		seconds = 10
	}

	return time.Duration(seconds) * time.Second
}

// DecideMove selects an unsolved cell and decides whether to submit the correct answer or a wrong answer based on bot accuracy
func (b *Bot) DecideMove(currentBoard [81]int, solution [81]int) (cell int, value int, isUnsolvable bool) {
	var unsolved []int
	for i := 0; i < 81; i++ {
		if currentBoard[i] == 0 {
			unsolved = append(unsolved, i)
		}
	}

	if len(unsolved) == 0 {
		return 0, 0, true
	}

	chosenCell := unsolved[b.rng.Intn(len(unsolved))]
	correctVal := solution[chosenCell]

	// Determine if bot makes a correct or wrong move based on accuracy
	if b.rng.Float64() <= b.Accuracy {
		return chosenCell, correctVal, false
	}

	// Submit wrong value (1-9 excluding correctVal)
	wrongVal := (correctVal % 9) + 1
	return chosenCell, wrongVal, false
}
