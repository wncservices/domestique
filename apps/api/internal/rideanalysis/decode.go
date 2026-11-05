package rideanalysis

import (
	"bytes"
	"context"
	"errors"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
	"github.com/muktihari/fit/proto"
)

// MaxMessages bounds how many FIT messages one file may carry. Byte limits
// alone do not bound memory: a 64 MiB entry of two-byte records is ~30M
// messages, each materialised as a struct, several GB with four imports at
// once. Resample refuses spans over 72 hours, and a 1 Hz recording of that is
// 259,200 records; laps, events, device info and HRV (a few per second) add
// to it. One million leaves ample headroom for any real ride while keeping a
// hostile file to tens of MB.
const MaxMessages = 1_000_000

// maxMessages is the cap DecodeFIT enforces; a variable only so a test can
// lower it instead of encoding a million messages.
var maxMessages = MaxMessages

// ErrTooManyMessages is returned when a FIT holds more than MaxMessages
// messages. Callers treat it as an unreadable file.
var ErrTooManyMessages = errors.New("rideanalysis: FIT has too many messages")

// activityBuilder receives each message as the decoder produces it, adds it
// to the Activity and stops the decode once the cap is passed, so the whole
// message list is never held alongside the typed one.
type activityBuilder struct {
	act    *filedef.Activity
	n      int
	over   bool
	cancel context.CancelFunc
}

func (b *activityBuilder) OnMesg(m proto.Message) {
	if b.over {
		return
	}
	b.n++
	if b.n > maxMessages {
		b.over = true
		b.cancel()
		return
	}
	b.act.Add(m)
}

// DecodeFIT decodes a FIT body into the *filedef.Activity that Analyze
// scores. It is the one decode path every caller shares: a ride fetched from
// a provider during sync and a ride read out of an uploaded export both go
// through it, so the two cannot drift in what they accept. It streams the
// messages and returns ErrTooManyMessages past MaxMessages.
func DecodeFIT(raw []byte) (*filedef.Activity, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &activityBuilder{act: filedef.NewActivity(), cancel: cancel}
	dec := decoder.New(bytes.NewReader(raw),
		decoder.WithMesgListener(b),
		decoder.WithBroadcastOnly(),
		// Broadcast-only hands the listener the decoder's own field buffers,
		// reused for the next message; Activity keeps some messages whole
		// (unrelated ones, developer fields), so each needs its own copy.
		decoder.WithBroadcastMesgCopy(),
	)
	_, err := dec.DecodeWithContext(ctx)
	if b.over {
		return nil, ErrTooManyMessages
	}
	if err != nil {
		return nil, err
	}
	return b.act, nil
}
