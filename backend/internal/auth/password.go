package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the work factor for password hashing. 10 is bcrypt's default
// and gives roughly 50-100ms per verification on current hardware, which is
// the right trade-off for an internal tool with interactive logins: slow
// enough to make offline cracking expensive, fast enough to keep login
// snappy.
const bcryptCost = 10

// HashPassword hashes a plaintext password with bcrypt (cost 10).
func HashPassword(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("auth: hash password: empty password")
	}
	// bcrypt silently truncates at 72 bytes; refuse instead so a long
	// passphrase does not appear to work with a prefix of itself.
	if len(plain) > 72 {
		return "", fmt.Errorf("auth: hash password: password longer than 72 bytes")
	}
	sum, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(sum), nil
}

// CheckPassword reports whether plain matches hash. An empty hash (OIDC-only
// account) never matches, and a malformed hash is treated as a mismatch rather
// than an error so the login path has one failure mode.
func CheckPassword(hash, plain string) bool {
	if hash == "" || plain == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}
