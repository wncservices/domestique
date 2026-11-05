package api

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/wncservices/domestique/apps/api/internal/garmin"
)

// A download goroutine is not a request goroutine, so a panic in it would end
// the process. Each one must come back as a failed download for its course,
// never as an empty success that imports a course with no track.

type panickingGarmin struct{ GarminConnector }

func (panickingGarmin) DownloadGPX(context.Context, GarminConsumer, garmin.Session, string) ([]byte, error) {
	panic("download exploded")
}

type panickingKomoot struct{ KomootImporter }

func (panickingKomoot) GPX(context.Context, string) ([]byte, error) {
	panic("download exploded")
}

func quietServer() *Server {
	return &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestGarminDownloadPanicIsAFailedDownload(t *testing.T) {
	s := quietServer()
	out := s.fetchGPX(t.Context(), panickingGarmin{}, GarminConsumer{}, garmin.Session{}, []string{"1", "2"}, 2)
	for _, id := range []string{"1", "2"} {
		if out[id].err == nil {
			t.Errorf("course %s: err nil after a panic, want a failed download", id)
		}
	}
}

func TestKomootDownloadPanicIsAFailedDownload(t *testing.T) {
	s := quietServer()
	out := s.fetchTours(t.Context(), panickingKomoot{}, []string{"1"}, 1)
	if out["1"].err == nil {
		t.Error("tour 1: err nil after a panic, want a failed download")
	}
}
