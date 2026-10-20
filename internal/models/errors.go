package models

type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}

type SizeErrorResponse struct {
	Error   string `json:"error"`
	MaxSize int64  `json:"max_size"`
}
