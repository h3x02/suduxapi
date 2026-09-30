package puzzle

import (
	"errors"
	"math/rand"
	"time"
)

type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
	DifficultyExpert Difficulty = "expert"
)

type Puzzle struct {
	Difficulty Difficulty `json:"difficulty"`
	Board      [81]int    `json:"board"`    // 0 for empty
	Solution   [81]int    `json:"solution"` // 1-9
}

type Generator struct {
	rng *rand.Rand
}

func NewGenerator() *Generator {
	return &Generator{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Generate creates a Sudoku puzzle with given difficulty
func (g *Generator) Generate(diff Difficulty) (*Puzzle, error) {
	var solution [81]int
	if !g.fillGrid(&solution) {
		return nil, errors.New("failed to generate solved sudoku grid")
	}

	board := solution
	var cluesToKeep int
	switch diff {
	case DifficultyEasy:
		cluesToKeep = 42
	case DifficultyMedium:
		cluesToKeep = 34
	case DifficultyHard:
		cluesToKeep = 28
	case DifficultyExpert:
		cluesToKeep = 24
	default:
		cluesToKeep = 34
	}

	indices := g.rng.Perm(81)
	toRemove := 81 - cluesToKeep

	for i := 0; i < toRemove; i++ {
		board[indices[i]] = 0
	}

	return &Puzzle{
		Difficulty: diff,
		Board:      board,
		Solution:   solution,
	}, nil
}

func (g *Generator) fillGrid(grid *[81]int) bool {
	emptyIdx := -1
	for i := 0; i < 81; i++ {
		if grid[i] == 0 {
			emptyIdx = i;
			break
		}
	}

	if emptyIdx == -1 {
		return true // solved
	}

	nums := g.rng.Perm(9)
	for _, n := range nums {
		val := n + 1
		if isValidPlacement(grid, emptyIdx, val) {
			grid[emptyIdx] = val
			if g.fillGrid(grid) {
				return true
			}
			grid[emptyIdx] = 0
		}
	}

	return false
}

func isValidPlacement(grid *[81]int, idx int, val int) bool {
	row := idx / 9
	col := idx % 9

	// Check row
	for c := 0; c < 9; c++ {
		if grid[row*9+c] == val {
			return false
		}
	}

	// Check column
	for r := 0; r < 9; r++ {
		if grid[r*9+col] == val {
			return false
		}
	}

	// Check 3x3 box
	boxRow := (row / 3) * 3
	boxCol := (col / 3) * 3
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			if grid[(boxRow+r)*9+(boxCol+c)] == val {
				return false
			}
		}
	}

	return true
}

func ValidateMove(solution *[81]int, cell int, val int) bool {
	if cell < 0 || cell >= 81 {
		return false
	}
	return solution[cell] == val
}
