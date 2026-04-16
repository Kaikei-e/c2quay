package lock_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Kaikei-e/c2quay/internal/lock"
)

func TestAcquire_ReleaseCycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.lock")
	l, err := lock.Acquire(path)
	require.NoError(t, err)
	require.NotNil(t, l)
	require.NoError(t, l.Release())
}

func TestAcquire_SecondFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.lock")
	l1, err := lock.Acquire(path)
	require.NoError(t, err)
	defer l1.Release()

	_, err = lock.Acquire(path)
	require.Error(t, err)
	assert.True(t, errors.Is(err, lock.ErrAlreadyHeld))
}

func TestAcquire_AfterReleaseSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "env.lock")
	l1, err := lock.Acquire(path)
	require.NoError(t, err)
	require.NoError(t, l1.Release())

	l2, err := lock.Acquire(path)
	require.NoError(t, err)
	require.NoError(t, l2.Release())
}
