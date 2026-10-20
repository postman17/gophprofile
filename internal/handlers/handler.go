package handlers

import (
	"io/fs"

	"github.com/postman17/gophprofile/internal/service"
)

type Handlers struct {
	Avatars *AvatarHandler
	Web     *WebHandler
	Health  *HealthHandler
}

func NewHandlers(svc *service.AvatarService, maxFileSize int64, templates fs.FS, checks map[string]Checker) (*Handlers, error) {
	webHandler, err := NewWebHandler(svc, maxFileSize, templates)
	if err != nil {
		return nil, err
	}
	return &Handlers{
		Avatars: NewAvatarHandler(svc, maxFileSize),
		Web:     webHandler,
		Health:  NewHealthHandler(checks),
	}, nil
}
