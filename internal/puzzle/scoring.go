package puzzle

type ScoringEngine interface {
	CalculateScore(currentCombo int, isCorrect bool) (points int, newCombo int)
}

type CellRaceScoring struct{}

func NewCellRaceScoring() *CellRaceScoring {
	return &CellRaceScoring{}
}

// CalculateScore implements Cell Race scoring rules:
// First consecutive correct cell: 100 points
// Second: 110
// Third: 120
// Formula: score = 100 + (combo - 1) * 10
// Wrong move resets combo to 0 and awards 0 points.
func (c *CellRaceScoring) CalculateScore(currentCombo int, isCorrect bool) (int, int) {
	if !isCorrect {
		return 0, 0
	}
	newCombo := currentCombo + 1
	points := 100 + (newCombo-1)*10
	return points, newCombo
}
