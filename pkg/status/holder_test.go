package status

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHolderUnavailable(t *testing.T) {
	var h Holder
	_, err := h.Snapshot(context.Background())
	require.ErrorIs(t, err, ErrUnavailable)
}

type stubReader struct {
	snap *Snapshot
}

func (s stubReader) Snapshot(context.Context) (*Snapshot, error) {
	return s.snap, nil
}

func TestHolderSet(t *testing.T) {
	var h Holder
	want := &Snapshot{Namespace: "ns"}
	h.Set(stubReader{snap: want})

	got, err := h.Snapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, want, got)
}
