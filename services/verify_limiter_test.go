package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVerifyLimiter(t *testing.T) {
	l, t0 := newVerifyLimiter(), time.Unix(0, 0)
	for i := 0; i < 20; i++ {
		assert.True(t, l.allow(uidA, t0.Add(time.Duration(i)*time.Minute)), i)
	}
	assert.False(t, l.allow(uidA, t0.Add(20*time.Minute)))
	assert.True(t, l.allow(uidB, t0.Add(20*time.Minute)))
	assert.True(t, l.allow(uidA, t0.Add(61*time.Minute))) // the oldest check has left the window
}
