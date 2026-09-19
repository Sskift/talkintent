package config

import (
	"path/filepath"
	"strings"
)

// AddWorkspace adds or updates a workspace in ClientConfig.
// It normalizes the root path, derives default name and ID if omitted,
// and ensures no duplicate entries by ID or root path.
func (c *ClientConfig) AddWorkspace(ws WorkspaceConfig) error {
	norm, err := NormalizeWorkspacePath(ws.RootPath)
	if err != nil {
		return err
	}
	ws.RootPath = norm

	if ws.Name == "" {
		ws.Name = filepath.Base(norm)
	}
	if ws.ID == "" {
		ws.ID = strings.ToLower(ws.Name)
	}

	for i, existing := range c.Workspaces {
		if existing.ID == ws.ID || filepath.Clean(existing.RootPath) == norm {
			c.Workspaces[i] = ws
			return nil
		}
	}

	c.Workspaces = append(c.Workspaces, ws)
	return nil
}

// RemoveWorkspace removes any workspace matching the target ID, name, or root path.
// Returns true if a matching workspace was found and removed.
func (c *ClientConfig) RemoveWorkspace(target string) bool {
	if c == nil || len(c.Workspaces) == 0 {
		return false
	}

	cleanTarget := filepath.Clean(strings.TrimSpace(target))
	var remaining []WorkspaceConfig
	removed := false

	for _, ws := range c.Workspaces {
		if ws.ID == target || ws.Name == target || filepath.Clean(ws.RootPath) == cleanTarget {
			removed = true
			continue
		}
		remaining = append(remaining, ws)
	}

	if removed {
		c.Workspaces = remaining
	}
	return removed
}

// FindWorkspace retrieves a workspace matching ID, name, or root path.
func (c *ClientConfig) FindWorkspace(target string) (*WorkspaceConfig, bool) {
	if c == nil {
		return nil, false
	}
	cleanTarget := filepath.Clean(strings.TrimSpace(target))
	for _, ws := range c.Workspaces {
		if ws.ID == target || ws.Name == target || filepath.Clean(ws.RootPath) == cleanTarget {
			wsCopy := ws
			return &wsCopy, true
		}
	}
	return nil, false
}
