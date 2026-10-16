package world

import (
	"errors"
	"math"
	"time"
)

type ClockState struct {
	originUTC       time.Time
	lastObservedUTC time.Time
	worldUTC        time.Time
	location        *time.Location
	scale           float64
}

func newClockState(input ClockInput) (ClockState, error) {
	if input.Location == nil || input.Location == time.Local || input.Location.String() == "Local" {
		return ClockState{}, errors.New("clock location must be an explicit resolved location")
	}
	if input.Scale <= 0 || math.IsNaN(input.Scale) || math.IsInf(input.Scale, 0) {
		return ClockState{}, errors.New("clock scale must be positive and finite")
	}
	origin := input.OriginUTC.Round(0).UTC()
	locationCopy := *input.Location
	return ClockState{
		originUTC:       origin,
		lastObservedUTC: origin,
		worldUTC:        origin,
		location:        &locationCopy,
		scale:           input.Scale,
	}, nil
}

func (c ClockState) UTC() time.Time {
	return c.worldUTC
}

func (c ClockState) Local() time.Time {
	if c.location == nil {
		return time.Time{}
	}
	locationCopy := *c.location
	return c.worldUTC.In(&locationCopy)
}

func (c ClockState) LastObservedUTC() time.Time {
	return c.lastObservedUTC
}

func (c ClockState) Advance(now time.Time) (ClockState, error) {
	if c.location == nil || c.scale <= 0 || math.IsNaN(c.scale) || math.IsInf(c.scale, 0) {
		return ClockState{}, errors.New("clock state is invalid")
	}
	nowUTC := now.Round(0).UTC()
	if nowUTC.Before(c.lastObservedUTC) {
		return ClockState{}, errors.New("clock rollback")
	}
	elapsed, err := durationBetween(c.originUTC, nowUTC)
	if err != nil {
		return ClockState{}, err
	}
	scaled := math.Round(float64(elapsed) * c.scale)
	if math.IsNaN(scaled) || math.IsInf(scaled, 0) || scaled < float64(math.MinInt64) || scaled >= float64(math.MaxInt64) {
		return ClockState{}, errors.New("scaled clock duration overflow")
	}
	c.lastObservedUTC = nowUTC
	c.worldUTC = c.originUTC.Add(time.Duration(scaled)).UTC()
	return c, nil
}

func durationBetween(start, end time.Time) (time.Duration, error) {
	delta := end.Sub(start)
	if !start.Add(delta).Equal(end) {
		return 0, errors.New("elapsed clock duration overflow")
	}
	return delta, nil
}
