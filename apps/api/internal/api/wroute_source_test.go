package api_test

import (
	"github.com/wncservices/domestique/apps/api/internal/routefixture"
	"github.com/wncservices/domestique/apps/api/internal/source"
)

// sourceCreate is a synthetic library route owned by owner: no real track.
func sourceCreate(name, owner string) source.CreateRequest {
	return source.CreateRequest{
		Name:       name,
		GPX:        routefixture.GPX(name, true, 50, 100, routefixture.Piece{LengthM: 3000, Grade: 1}),
		UploadedBy: owner,
	}
}
