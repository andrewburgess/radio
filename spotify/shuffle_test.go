package spotify

import (
	"slices"
	"testing"
)

// applyMoves simulates executing the reorder moves against a slice in memory,
// applying the same semantics as the Spotify reorder API: remove item at
// rangeStart then insert it before insertBefore in the remaining slice.
func applyMoves(order []string, moves [][2]int) []string {
	s := slices.Clone(order)
	for _, move := range moves {
		rangeStart, insertBefore := move[0], move[1]
		item := s[rangeStart]
		s = slices.Delete(s, rangeStart, rangeStart+1)
		s = slices.Insert(s, insertBefore, item)
	}
	return s
}

func TestComputeReorderMoves(t *testing.T) {
	tests := []struct {
		name    string
		current []string
		target  []string
	}{
		{
			name:    "empty",
			current: []string{},
			target:  []string{},
		},
		{
			name:    "single",
			current: []string{"A"},
			target:  []string{"A"},
		},
		{
			name:    "already sorted",
			current: []string{"A", "B", "C", "D"},
			target:  []string{"A", "B", "C", "D"},
		},
		{
			name:    "reverse",
			current: []string{"A", "B", "C", "D"},
			target:  []string{"D", "C", "B", "A"},
		},
		{
			name:    "swap adjacent",
			current: []string{"A", "B", "C"},
			target:  []string{"B", "A", "C"},
		},
		{
			name:    "rotate left",
			current: []string{"A", "B", "C", "D", "E"},
			target:  []string{"B", "C", "D", "E", "A"},
		},
		{
			name:    "move first to last",
			current: []string{"A", "B", "C", "D", "E"},
			target:  []string{"B", "C", "D", "E", "A"},
		},
		{
			name:    "move last to first",
			current: []string{"A", "B", "C", "D", "E"},
			target:  []string{"E", "A", "B", "C", "D"},
		},
		{
			name:    "arbitrary permutation",
			current: []string{"A", "B", "C", "D", "E"},
			target:  []string{"C", "A", "E", "B", "D"},
		},
		{
			name:    "two elements swapped",
			current: []string{"A", "B"},
			target:  []string{"B", "A"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			moves := computeReorderMoves(tc.current, tc.target)
			got := applyMoves(tc.current, moves)
			if !slices.Equal(got, tc.target) {
				t.Errorf("after applying moves: got %v, want %v (moves: %v)", got, tc.target, moves)
			}
		})
	}
}

func TestComputeReorderMovesNoMovesWhenAlreadySorted(t *testing.T) {
	current := []string{"A", "B", "C", "D", "E"}
	moves := computeReorderMoves(current, current)
	if len(moves) != 0 {
		t.Errorf("expected no moves for already-sorted list, got %v", moves)
	}
}

func TestComputeReorderMovesMismatchedLength(t *testing.T) {
	moves := computeReorderMoves([]string{"A", "B"}, []string{"A"})
	if moves != nil {
		t.Errorf("expected nil for mismatched lengths, got %v", moves)
	}
}
