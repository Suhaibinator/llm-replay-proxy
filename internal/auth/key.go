package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MinKeyBytes is the minimum HMAC-SHA256 signing key size.
const MinKeyBytes = 32

// generatedKeyBytes is the size of keys created by GenerateKey.
const generatedKeyBytes = 32

// Key is an HMAC signing key. It never prints its contents, so a
// configuration value that holds one can be logged or formatted safely.
type Key struct{ b []byte }

const redacted = "[REDACTED]"

func (k Key) String() string             { return redacted }
func (k Key) GoString() string           { return redacted }
func (k Key) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(redacted)) }
func (k Key) MarshalText() ([]byte, error) {
	return nil, errors.New("signing keys cannot be serialized")
}

// IsZero reports whether no key material is present.
func (k Key) IsZero() bool { return len(k.b) == 0 }

// ParseKey decodes base64 key text (standard or URL alphabet, padding
// optional, surrounding whitespace ignored) and enforces MinKeyBytes.
func ParseKey(text string) (Key, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Key{}, errors.New("signing key is empty")
	}
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err = enc.DecodeString(text); err == nil {
			break
		}
	}
	if err != nil {
		return Key{}, errors.New("signing key must be base64 encoded")
	}
	if len(raw) < MinKeyBytes {
		return Key{}, fmt.Errorf("signing key must decode to at least %d bytes", MinKeyBytes)
	}
	return Key{b: raw}, nil
}

// GenerateKey returns a new random key.
func GenerateKey() (Key, error) {
	raw := make([]byte, generatedKeyBytes)
	if _, err := rand.Read(raw); err != nil {
		return Key{}, fmt.Errorf("generate signing key: %w", err)
	}
	return Key{b: raw}, nil
}

// encode returns the base64 text form used in key files.
func (k Key) encode() string { return base64.StdEncoding.EncodeToString(k.b) }

// DefaultKeyPath is where a generated key is kept when nothing is configured:
// next to the SQLite database, so one installation keeps one key.
func DefaultKeyPath(database string) string { return database + ".jwt-key" }

// ReadKeyFile reads a base64 key file.
func ReadKeyFile(path string) (Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Key{}, fmt.Errorf("read signing key file: %w", err)
	}
	k, err := ParseKey(string(raw))
	if err != nil {
		return Key{}, fmt.Errorf("signing key file %s: %w", path, err)
	}
	return k, nil
}

// LoadOrCreateKeyFile reads the key at path, creating it with a fresh random
// key (mode 0600) when it does not exist. created reports whether a new key
// was written.
func LoadOrCreateKeyFile(path string) (k Key, created bool, err error) {
	k, err = ReadKeyFile(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return k, false, err
	}
	k, err = GenerateKey()
	if err != nil {
		return Key{}, false, err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Key{}, false, fmt.Errorf("create signing key directory: %w", err)
		}
	}
	// Write a complete private temporary file, then hard-link it into place.
	// Linking fails if the target exists, so concurrent first starts agree on
	// one key and nobody can observe a partially written file.
	f, err := os.CreateTemp(filepath.Dir(path), ".jwt-key-*")
	if err != nil {
		return Key{}, false, fmt.Errorf("create signing key file: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	werr := f.Chmod(0o600)
	if werr == nil {
		_, werr = f.WriteString(k.encode() + "\n")
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return Key{}, false, fmt.Errorf("write signing key file: %w", werr)
	}
	if err := os.Link(tmp, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			k, err = ReadKeyFile(path)
			return k, false, err
		}
		return Key{}, false, fmt.Errorf("create signing key file: %w", err)
	}
	return k, true, nil
}
