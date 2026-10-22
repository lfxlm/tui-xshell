package model

import "time"

// WorkspaceLayout stores a versioned JSON snapshot of terminal tab/pane
// topology. The snapshot references saved host IDs and must never contain
// credentials, private-key data, or terminal output.
type WorkspaceLayout struct {
	Id        string    `json:"id"`
	Name      string    `json:"name"`
	Data      string    `json:"data"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ShortcutBar is a named, ordered set of saved hosts used by the home-screen
// connection shortcuts. HostIDs are local references and are not exported.
type ShortcutBar struct {
	Id        string   `json:"id"`
	Name      string   `json:"name"`
	SortOrder int      `json:"sort_order"`
	HostIDs   []string `json:"host_ids"`
}
