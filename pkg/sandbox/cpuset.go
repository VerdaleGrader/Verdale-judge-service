package sandbox

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ParseCPUList parses the kernel's CPU list format, e.g. "2-5,8", into a
// sorted list of distinct core numbers. An empty string means "no list".
func ParseCPUList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		first, last, isRange := strings.Cut(part, "-")
		start, err := strconv.Atoi(first)
		if err != nil || start < 0 {
			return nil, fmt.Errorf("cpu list %q: %q is not a core number", raw, first)
		}
		end := start
		if isRange {
			if end, err = strconv.Atoi(last); err != nil || end < start {
				return nil, fmt.Errorf("cpu list %q: %q is not a valid range", raw, part)
			}
		}
		if end-start > 4096 {
			return nil, fmt.Errorf("cpu list %q: range %q is too large", raw, part)
		}
		for core := start; core <= end; core++ {
			seen[core] = true
		}
	}
	cores := make([]int, 0, len(seen))
	for core := range seen {
		cores = append(cores, core)
	}
	sort.Ints(cores)
	return cores, nil
}

// FormatCPUList is the inverse of ParseCPUList for a single core.
func formatCore(core int) string { return strconv.Itoa(core) }
