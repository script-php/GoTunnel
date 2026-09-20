package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// HashPassword creates a salted SHA256 hash of the password
func HashPassword(password string) string {
	// Generate a random 16-byte salt
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		// Fallback: use a deterministic salt based on password length
		for i := range salt {
			salt[i] = byte(len(password) * 37 % 256)
		}
	}

	// Combine salt + password and hash
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	hash := h.Sum(nil)

	// Return salt:hash for storage
	return hex.EncodeToString(salt) + ":" + hex.EncodeToString(hash)
}

// VerifyPassword checks if a password matches its salted hash
func VerifyPassword(password, storedHash string) bool {
	// Parse salt from stored hash
	parts := make([]string, 0)
	var current string
	for i, c := range storedHash {
		if c == ':' {
			parts = append(parts, current)
			current = ""
		} else {
			current += string(c)
			if i == len(storedHash)-1 {
				parts = append(parts, current)
			}
		}
	}

	if len(parts) != 2 {
		return false
	}

	saltHex := parts[0]
	hashHex := parts[1]

	// Decode salt
	salt, err := hex.DecodeString(saltHex)
	if err != nil || len(salt) != 16 {
		return false
	}

	// Hash the provided password with the same salt
	h := sha256.New()
	h.Write(salt)
	h.Write([]byte(password))
	computedHash := h.Sum(nil)

	// Compare computed hash with stored hash
	return hex.EncodeToString(computedHash) == hashHex
}

// Authenticator handles authentication logic
type Authenticator struct {
	serverPassword string
}

// NewAuthenticator creates a new authenticator with server password
func NewAuthenticator(serverPassword string) *Authenticator {
	return &Authenticator{
		serverPassword: serverPassword,
	}
}

// Authenticate verifies client password
func (a *Authenticator) Authenticate(clientPassword string) error {
	if clientPassword != a.serverPassword {
		return fmt.Errorf("invalid password")
	}
	return nil
}
