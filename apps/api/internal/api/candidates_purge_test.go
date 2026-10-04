package api

import (
	"context"
	"testing"
	"time"
)

func TestPurgingARiderDropsTheirHeldCandidates(t *testing.T) {
	now := candidatesT0
	srv := &Server{Clock: func() time.Time { return now }}
	mine := srv.candidateStore().Hold("wilant", "w1", heldOf("w1", 3))
	theirs := srv.candidateStore().Hold("marie", "w2", heldOf("w2", 3))

	if _, err := srv.purgeRiderData(context.Background(), "Wilant"); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.candidateStore().Get("wilant", mine[0]); ok {
		t.Error("a purged rider's route candidates are still in memory")
	}
	if _, ok := srv.candidateStore().Get("marie", theirs[0]); !ok {
		t.Error("purging one rider dropped another's candidates")
	}
}
