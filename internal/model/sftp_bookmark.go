package model

// SFTPBookmark is a local-only shortcut to a local or remote file directory.
// ScopeKey groups remote bookmarks by saved host or endpoint identity.
type SFTPBookmark struct {
	Id       string `json:"id"`
	ScopeKey string `json:"-"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Remote   bool   `json:"remote"`
}
