package rideanalysis

import (
	"bytes"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/filedef"
)

// DecodeFIT decodes a FIT body into the *filedef.Activity that Analyze
// scores. It is the one decode path every caller shares: a ride fetched from
// a provider during sync and a ride read out of an uploaded export both go
// through it, so the two cannot drift in what they accept.
func DecodeFIT(raw []byte) (*filedef.Activity, error) {
	fit, err := decoder.New(bytes.NewReader(raw)).Decode()
	if err != nil {
		return nil, err
	}
	return filedef.NewActivity(fit.Messages...), nil
}
