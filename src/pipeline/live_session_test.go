package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeLiveSessionSource struct{}

type fakeLiveSessionSourceWithWrongSignature struct{}

type fakeLiveSession struct {
	ID        int64
	StartTime time.Time
	EndTime   time.Time
}

func (fakeLiveSessionSource) GetSessionsByLiveID(ctx context.Context, liveID string, limit int) ([]fakeLiveSession, error) {
	return []fakeLiveSession{
		{ID: 42, StartTime: time.Date(2026, 6, 1, 18, 0, 0, 0, time.Local)},
	}, nil
}

func (fakeLiveSessionSourceWithWrongSignature) GetSessionsByLiveID(ctx context.Context, liveID int, limit int) ([]fakeLiveSession, error) {
	return []fakeLiveSession{{ID: 99}}, nil
}

func TestLiveSessionIDFromValueReadsLatestSessionIDWithoutLivestateImport(t *testing.T) {
	assert.Equal(t, "42", liveSessionIDFromValue(context.Background(), fakeLiveSessionSource{}, "bilibili:linkai"))
}

func TestLiveSessionIDFromValueIgnoresIncompatibleMethodSignature(t *testing.T) {
	assert.Equal(t, "", liveSessionIDFromValue(context.Background(), fakeLiveSessionSourceWithWrongSignature{}, "bilibili:linkai"))
}
