package commands

import "strings"

// todoTargetID returns the ID from an explicit id:<id> target. The second
// result stays true for an empty ID so callers can reject malformed explicit
// targets without falling back to a list lookup.
func todoTargetID(target string) (string, bool) {
	id, explicit := strings.CutPrefix(strings.TrimSpace(target), "id:")
	if !explicit {
		return "", false
	}
	return strings.TrimSpace(id), true
}

func todoTargetItem(target string) (map[string]any, bool) {
	id, explicit := todoTargetID(target)
	if !explicit || id == "" {
		return nil, false
	}
	return map[string]any{"id": id}, true
}

func allTodoTargetsExplicitIDs(targets []string) bool {
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if _, explicit := todoTargetID(target); !explicit {
			return false
		}
	}
	return true
}
