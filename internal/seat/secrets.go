package seat

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
)

// Secrets are the per seat credentials the daemon owns.
//
// Kept apart from the seat definition rather than being another field on it.
// The definition is what the interface reads on every refresh, and a password
// has no business travelling with it; it is fetched on demand instead, from
// its own endpoint.
type Secrets struct {
	// SunshineUser and SunshinePassword are what Sunshine's own web interface
	// inside the seat asks for. The daemon sets them while provisioning rather
	// than asking somebody to, because it needs them itself: pairing a device
	// from one place means the daemon talking to each seat's Sunshine on its
	// behalf.
	SunshineUser     string `json:"sunshine_user"`
	SunshinePassword string `json:"sunshine_password"`
}

func (s *Store) secretsPath(name string) string {
	return filepath.Join(s.dir, "..", "secrets", name+".json")
}

// Secrets returns a seat's credentials, empty if none have been generated yet.
func (s *Store) Secrets(name string) (Secrets, error) {
	var out Secrets

	data, err := os.ReadFile(s.secretsPath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}

		return out, err
	}

	return out, json.Unmarshal(data, &out)
}

// PutSecrets writes a seat's credentials, readable only by root.
func (s *Store) PutSecrets(name string, secrets Secrets) error {
	path := s.secretsPath(name)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(secrets, "", "  ")
	if err != nil {
		return err
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp, path)
}

// DeleteSecrets forgets a seat's credentials.
func (s *Store) DeleteSecrets(name string) error {
	err := os.Remove(s.secretsPath(name))
	if os.IsNotExist(err) {
		return nil
	}

	return err
}

// RandomPassword returns a password for a seat's Sunshine interface.
//
// Long and random because nobody has to remember it: the daemon stores it and
// shows it when asked. It still ends up guarding a web interface on the LAN.
func RandomPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}
