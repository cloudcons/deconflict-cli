package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cloudcons/agentclaims/internal/claim"
	"github.com/cloudcons/agentclaims/internal/settings"
)

// FileStore is an append-only JSONL log on local disk.
type FileStore struct{ Path string }

func (f *FileStore) Describe() string { return "file:" + f.Path }

// Settings reads the sibling settings.json, falling back to defaults.
func (f *FileStore) Settings() settings.Settings {
	return settings.NewStore(f.Path).Load()
}

func (f *FileStore) Append(e claim.Event) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(f.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer fh.Close()
	// O_APPEND already makes a sub-page write atomic on Linux and macOS;
	// flock is belt-and-braces for network filesystems.
	if err := syscall.Flock(int(fh.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock %s: %w", f.Path, err)
	}
	defer syscall.Flock(int(fh.Fd()), syscall.LOCK_UN)

	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fh.Write(append(line, '\n'))
	return err
}

func (f *FileStore) Events() ([]claim.Event, error) {
	fh, err := os.Open(f.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer fh.Close()
	return ReadEvents(fh)
}

// ReadEvents parses a JSONL event stream, skipping unparseable lines. A
// corrupt line must never make the whole registry unreadable — losing one
// claim is a missed warning, losing the log is an outage.
func ReadEvents(fh *os.File) ([]claim.Event, error) {
	var out []claim.Event
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e claim.Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out, sc.Err()
}
