package status

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoSnapshot(t *testing.T) {
	snap := DemoSnapshot()
	require.NotEmpty(t, snap.Namespace)
	require.NotEmpty(t, snap.Groups)

	var reader DemoReader
	got, err := reader.Snapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, snap.Namespace, got.Namespace)
	require.Len(t, got.Groups, len(snap.Groups))
}
