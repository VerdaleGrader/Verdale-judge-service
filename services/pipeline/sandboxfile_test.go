package pipeline

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A program that replaced its output with a link to a host file must not
// make Judge read that file.
func TestLinksAndPipesInPlaceOfOutputAreNotFollowed(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("host secret"), 0o600))

	link := filepath.Join(dir, "output.txt")
	require.NoError(t, os.Symlink(secret, link))
	data, err := readPrefix(link, 100)
	require.NoError(t, err)
	assert.Empty(t, data, "a link reads as an absent file")
	_, ok := regularFileSize(link)
	assert.False(t, ok)
	assert.True(t, (&capture{stdout: 100, remaining: 100}).fits(link), "an absent file fits")
	text, size, ok := (&capture{stdout: 100, remaining: 100}).take(link, 100, false)
	assert.True(t, ok)
	assert.Empty(t, text)
	assert.Zero(t, size)

	pipe := filepath.Join(dir, "pipe.txt")
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))
	data, err = readPrefix(pipe, 100) // would block forever if the pipe were opened for reading
	require.NoError(t, err)
	assert.Empty(t, data)

	regular := filepath.Join(dir, "regular.txt")
	require.NoError(t, os.WriteFile(regular, []byte("hello"), 0o600))
	data, err = readPrefix(regular, 100)
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	missing, err := readPrefix(filepath.Join(dir, "none"), 100)
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestCopyFileRefusesALink(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("host secret"), 0o600))
	link := filepath.Join(dir, "main")
	require.NoError(t, os.Symlink(secret, link))

	assert.Error(t, copyFile(link, filepath.Join(dir, "copy"), 0o755), "a compiled program that is a link is not copied")
}
