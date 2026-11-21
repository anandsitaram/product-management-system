package security

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

var fast = Argon2Params{Memory: 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

func TestPasswordHashing(t *testing.T) {
	h1, err := HashPassword("correct horse 1", fast)
	require.NoError(t, err)
	h2, _ := HashPassword("correct horse 1", fast)
	require.NotEqual(t, h1, h2, "salts differ")
	require.Contains(t, h1, "$argon2id$v=19$m=1024,t=1,p=1$")
	ok, err := VerifyPassword("correct horse 1", h1)
	require.NoError(t, err)
	require.True(t, ok)
	ok, _ = VerifyPassword("wrong", h1)
	require.False(t, ok)
	_, err = VerifyPassword("x", "not-a-hash")
	require.Error(t, err)
}

func TestAccessTokens(t *testing.T) {
	tm := NewTokenManager("0123456789abcdef0123456789abcdef", "iss", time.Minute)
	id := uuid.New()
	tok, _, err := tm.IssueAccess(id, "ADMIN")
	require.NoError(t, err)
	c, err := tm.VerifyAccess(tok)
	require.NoError(t, err)
	require.Equal(t, id, c.UserID)
	require.Equal(t, "ADMIN", c.Role)

	_, err = NewTokenManager("another-secret-another-secret-1234", "iss", time.Minute).VerifyAccess(tok)
	require.Error(t, err)
	_, err = NewTokenManager("0123456789abcdef0123456789abcdef", "other", time.Minute).VerifyAccess(tok)
	require.Error(t, err, "issuer mismatch")
	old, _, _ := NewTokenManager("0123456789abcdef0123456789abcdef", "iss", -time.Second).IssueAccess(id, "ADMIN")
	_, err = tm.VerifyAccess(old)
	require.Error(t, err, "expired")
	// alg=none must be refused
	_, err = tm.VerifyAccess("eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJ4In0.")
	require.Error(t, err)
}

func TestRefreshTokens(t *testing.T) {
	tok, hash, err := NewRefreshToken()
	require.NoError(t, err)
	require.Equal(t, hash, HashToken(tok))
	require.NotEqual(t, tok, hash)
	tok2, _, _ := NewRefreshToken()
	require.NotEqual(t, tok, tok2)
}
