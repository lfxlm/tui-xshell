package model

// QuickCommand stores a reusable command snippet for terminal sessions.
type QuickCommand struct {
	Id      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
}
