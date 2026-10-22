package model

type Group struct {
	Id       string `json:"id"`
	Name     string `json:"name"`
	ParentId string `json:"parent_id"`
	// AuthProfileID is a local inheritance rule and is excluded from portable
	// session catalogs because profiles contain machine-local references.
	AuthProfileID string `json:"-"`
	// LocalStartDir is machine-specific and stays out of portable catalogs.
	LocalStartDir string `json:"-"`
	// RemoteStartDir is portable session configuration inherited by nested groups.
	RemoteStartDir string `json:"remote_start_dir,omitempty"`
}
