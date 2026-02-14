package helpers

// =============================================================================
// Smart Grid Layout
// =============================================================================
// Returns a sensible (rows, cols) for N camera widgets.
// Matches Python's get_smart_grid() from ui/layout.py.
// =============================================================================

// GetSmartGrid returns optimal (rows, cols) for n camera widgets.
// Handles 1-9 cameras with hardcoded optimal layouts, and 10+
// with a dynamic formula capping at 4 columns.
func GetSmartGrid(n int) (rows, cols int) {
	switch {
	case n <= 1:
		return 1, 1
	case n == 2:
		return 1, 2
	case n == 3:
		return 1, 3
	case n == 4:
		return 2, 2
	case n <= 6:
		return 2, 3
	case n <= 9:
		return 3, 3
	default:
		// 10+ cameras: cols = min(4, floor(sqrt(n) * 1.5))
		// rows = ceil(n / cols)
		cols = int(float64(isqrt(n)) * 1.5)
		if cols > 4 {
			cols = 4
		}
		if cols < 1 {
			cols = 1
		}
		rows = (n + cols - 1) / cols
		return rows, cols
	}
}

// isqrt returns the integer square root of n.
func isqrt(n int) int {
	if n <= 0 {
		return 0
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}
