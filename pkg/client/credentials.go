package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Where a signed-in CLI keeps its token.
//
// Per-server rather than one global value, because a laptop that works on two
// teams' registries is normal and a single DECONFLICT_TOKEN would have it
// sending one team's credential to the other. The environment variable still
// wins when set: that is what CI uses, and an explicit variable should always
// beat a file somebody forgot about.

// CredentialsPath is the file `deconflict login` writes.
func CredentialsPath() string {
	if p := os.Getenv("DECONFLICT_CREDENTIALS"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "deconflict", "credentials.json")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "deconflict", "credentials.json")
}

type credentials struct {
	// Servers maps a registry base URL to its token.
	Servers map[string]string `json:"servers"`
}

var credMu sync.Mutex

func loadCredentials() credentials {
	c := credentials{Servers: map[string]string{}}
	b, err := os.ReadFile(CredentialsPath())
	if err != nil {
		return c
	}
	if err := json.Unmarshal(b, &c); err != nil || c.Servers == nil {
		return credentials{Servers: map[string]string{}}
	}
	return c
}

// TokenFor resolves the credential for a registry: the environment first, then
// the credentials file.
func TokenFor(base string) string {
	if v := strings.TrimSpace(os.Getenv("DECONFLICT_TOKEN")); v != "" {
		return v
	}
	credMu.Lock()
	defer credMu.Unlock()
	return loadCredentials().Servers[normalizeBase(base)]
}

// SaveToken records a token for one registry. The file is written 0600 and
// through a temporary file, because a half-written credentials file is a locked
// out laptop.
func SaveToken(base, token string) error {
	credMu.Lock()
	defer credMu.Unlock()
	c := loadCredentials()
	c.Servers[normalizeBase(base)] = token
	return writeCredentials(c)
}

// ForgetToken removes one registry's credential, or all of them when base is
// empty.
func ForgetToken(base string) error {
	credMu.Lock()
	defer credMu.Unlock()
	c := loadCredentials()
	if base == "" {
		c.Servers = map[string]string{}
	} else {
		delete(c.Servers, normalizeBase(base))
	}
	return writeCredentials(c)
}

func writeCredentials(c credentials) error {
	path := CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func normalizeBase(base string) string { return strings.TrimRight(strings.TrimSpace(base), "/") }
