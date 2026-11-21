package storage

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalKeysAreConfined(t *testing.T) {
	l, err := NewLocal(t.TempDir(), "http://x", "secret")
	require.NoError(t, err)
	ctx := context.Background()
	for _, k := range []string{"../evil", "a/../../evil", "", "/", `a\b`} {
		require.Error(t, l.Put(ctx, k, bytes.NewReader([]byte("x")), 1, "image/png"), "key %q", k)
	}
	require.NoError(t, l.Put(ctx, "products/p/ok.png", bytes.NewReader([]byte("x")), 1, "image/png"))
	require.NoError(t, l.Delete(ctx, "products/p/ok.png"))
	require.NoError(t, l.Delete(ctx, "products/p/missing.png"), "delete is idempotent")
}
