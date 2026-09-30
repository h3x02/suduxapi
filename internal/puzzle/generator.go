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

// Generate creates a Sudoku puzzle with the given difficulty that is guaranteed
// to have exactly one solution. Clues are removed one by one and each removal is
// verified against a solution counter; removals that create a second solution are
// rolled back. If the target clue count is not reachable while keeping the puzzle
// unique (common for expert), the loop stops at the first difficulty level that
// fails and returns the unique puzzle with the fewest clues achieved.
func (g *Generator) Generate(diff Difficulty) (*Puzzle, error) {
	var solution [81]int
	if !g.fillGrid(&solution) {
		return nil, errors.New("failed to generate solved sudoku grid")
	}

	cluesToKeep := cluesForDifficulty(diff)

	board := solution
	remaining := 81

	for _, idx := range g.rng.Perm(81) {
		if remaining <= cluesToKeep {
			break
		}
		if board[idx] == 0 {
			continue
		}

		removed := board[idx]
		board[idx] = 0

		// Keep the removal only if the puzzle still has exactly one solution.
		if g.countSolutions(&board, 2) != 1 {
			board[idx] = removed
		} else {
			remaining--
		}
	}

	return &Puzzle{
		Difficulty: diff,
		Board:      board,
		Solution:   solution,
	}, nil
}

func cluesForDifficulty(diff Difficulty) int {
	switch diff {
	case DifficultyEasy:
		return 42
	case DifficultyMedium:
		return 34
	case DifficultyHard:
		return 28
	case DifficultyExpert:
		return 24
	default:
		return 34
	}
}

// CountSolutionsForTest exposes the solution counter for tests.
func (g *Generator) CountSolutionsForTest(grid *[81]int, limit int) int {
	return g.countSolutions(grid, limit)
}

// countSolutions counts up to `limit` solutions of the given grid (0 = empty).
// It stops as soon as `limit` solutions are found. The branch cell is chosen
// with the MRV heuristic (fewest candidates first), which keeps counting fast
// even on sparse expert puzzles where a naive first-empty-cell search explodes
// combinatorially.
func (g *Generator) countSolutions(grid *[81]int, limit int) int {
	if limit <= 0 {
		return 0
	}

	best := -1
	bestCandidates := make([]int, 0, 9)
	var candidates [9]int

	for i := 0; i < 81; i++ {
		if grid[i] != 0 {
			continue
		}
		n := 0
		for _, v := range g.rng.Perm(9) {
			val := v + 1
			if isValidPlacement(grid, i, val) {
				candidates[n] = val
				n++
			}
		}
		if n == 0 {
			return 0 // dead end: some empty cell has no candidates
		}
		if best == -1 || n < len(bestCandidates) {
			best = i
			bestCandidates = bestCandidates[:0]
			bestCandidates = append(bestCandidates, candidates[:n]...)
			if n == 1 {
				break // cannot do better than a forced cell
			}
		}
	}

	if best == -1 {
		return 1 // no empty cells: this grid is a solution
	}

	count := 0
	for _, val := range bestCandidates {
		grid[best] = val
		count += g.countSolutions(grid, limit-count)
		grid[best] = 0
		if count >= limit {
			return count
		}
	}
	return count
}

func (g *Generator) fillGrid(grid *[81]int) bool {
	emptyIdx := -1
	for i := 0; i < 81; i++ {
		if grid[i] == 0 {
			emptyIdx = i
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
