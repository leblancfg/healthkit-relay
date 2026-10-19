package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The admin token signs in to the setup page and authorizes the admin API (pairing codes,
// devices, tokens). The server generates it on first start (256 random bits), prints it
// once, and keeps only its SHA-256 in <archive-dir>/admin.json:
//
//	{"token_sha256": "9f86d0...", "created_at": "2026-10-19T12:00:00Z"}
//
// A plain hash suits a random 256-bit token: there is nothing to guess from it.
// `healthkit-relay admin reset` writes a new one; the running server rereads the file when it
// changes, so the old token stops working at once.

const adminFile = "admin.json"

type adminRecord struct {
	TokenSHA256 string    `json:"token_sha256"`
	CreatedAt   time.Time `json:"created_at"`
}

// AdminKey checks the admin token against the hash in admin.json.
type AdminKey struct {
	path string // "" for a key held in memory only (tests)

	mu    sync.Mutex
	hash  []byte
	stamp stamp
}

// NewAdminKey is a key for token held in memory only.
func NewAdminKey(token string) *AdminKey {
	sum := sha256.Sum256([]byte(token))
	return &AdminKey{hash: sum[:]}
}

// LoadAdminKey reads dir/admin.json; ok is false when there is none.
func LoadAdminKey(dir string) (k *AdminKey, ok bool, err error) {
	k = &AdminKey{path: filepath.Join(dir, adminFile)}
	if err := k.reload(); errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

func (k *AdminKey) reload() error {
	b, err := os.ReadFile(k.path)
	if err != nil {
		return err
	}
	var rec adminRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return fmt.Errorf("%s: %w", k.path, err)
	}
	h, err := hex.DecodeString(rec.TokenSHA256)
	if err != nil || len(h) != sha256.Size {
		return fmt.Errorf("%s: token_sha256 is not a SHA-256", k.path)
	}
	k.hash = h
	k.stamp.set(k.path)
	return nil
}

// current is the hash in effect, after rereading admin.json if it changed.
func (k *AdminKey) current() []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.path != "" {
		if changed, _ := k.stamp.changed(k.path); changed {
			if err := k.reload(); err != nil {
				log.Printf("admin token: keeping the previous one: %v", err)
			}
		}
	}
	return k.hash
}

// Check tells whether token is the admin token, in constant time.
func (k *AdminKey) Check(token string) bool {
	if k == nil || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], k.current()) == 1
}

// ID names the admin token in effect (its hash, hex): setup sessions remember it, so that a
// reset signs them out.
func (k *AdminKey) ID() string {
	if k == nil {
		return ""
	}
	return hex.EncodeToString(k.current())
}

// writeAdminToken stores the hash of token in dir/admin.json.
func writeAdminToken(dir, token string) error {
	path := filepath.Join(dir, adminFile)
	unlock, err := lockState(path)
	if err != nil {
		return err
	}
	defer unlock()
	b, _ := json.MarshalIndent(adminRecord{TokenSHA256: hashToken(token), CreatedAt: time.Now().UTC().Truncate(time.Second)}, "", "  ")
	return writeFileAtomic(path, append(b, '\n'))
}

// newAdminToken generates an admin token (256 random bits) and stores its hash.
func newAdminToken(dir string) (string, error) {
	token := randomString(32)
	return token, writeAdminToken(dir, token)
}

// openAdmin loads the admin key at server start. Without admin.json it imports -token-file
// (the server token of earlier versions) or generates a token and prints it, with the setup link, to log.
func openAdmin(dir, tokenFile, setupURL string, log *log.Logger) (*AdminKey, error) {
	k, ok, err := LoadAdminKey(dir)
	if err != nil {
		return nil, err
	}
	if tokenFile != "" {
		token, err := readSecret(tokenFile)
		if err != nil {
			return nil, err
		}
		switch {
		case ok && k.Check(token):
		case ok:
			log.Printf("WARNING: -token-file %s is ignored: %s holds another admin token. Delete the file and remove -token-file (HKR_TOKEN_FILE).", tokenFile, adminFile)
			return k, nil
		case len(token) < 16:
			return nil, errors.New("the token in -token-file is shorter than 16 characters")
		default:
			if err := writeAdminToken(dir, token); err != nil {
				return nil, err
			}
			log.Printf("admin token: imported the hash of -token-file %s into %s", tokenFile, filepath.Join(dir, adminFile))
		}
		log.Printf("WARNING: -token-file (HKR_TOKEN_FILE) is deprecated: %s holds the admin token's hash. "+
			"Save the token in a password manager, then delete %s and remove -token-file. "+
			"New admin token: healthkit-relay admin reset", adminFile, tokenFile)
		k, _, err = LoadAdminKey(dir)
		return k, err
	}
	if ok {
		return k, nil
	}
	token, err := newAdminToken(dir)
	if err != nil {
		return nil, err
	}
	printAdminToken(log.Writer(), token, setupURL, "This server's admin token was generated.")
	return requireAdminKey(dir)
}

// requireAdminKey is LoadAdminKey for an admin.json that must exist.
func requireAdminKey(dir string) (*AdminKey, error) {
	k, ok, err := LoadAdminKey(dir)
	if err == nil && !ok {
		err = fmt.Errorf("%s: %w", filepath.Join(dir, adminFile), os.ErrNotExist)
	}
	return k, err
}

func printAdminToken(w io.Writer, token, setupURL, first string) {
	fmt.Fprintf(w, "\n%s It is shown only this once; the server keeps only its hash.\n\n", first)
	fmt.Fprintf(w, "    Admin token: %s\n", token)
	if setupURL != "" {
		fmt.Fprintf(w, "    Sign in at:  %s\n", setupURL)
	}
	fmt.Fprintf(w, "\nSave it in a password manager. It signs in to the setup page, where you pair iPhones and create read tokens.\n")
	fmt.Fprintf(w, "Lost it? Run healthkit-relay admin reset for a new one.\n\n")
}
