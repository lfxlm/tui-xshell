package model

// SFTPFilter is a reusable filename/type filter for either file pane.
type SFTPFilter struct {
	Id         string `json:"id"`
	Name       string `json:"name"`
	Expression string `json:"expression"`
}
