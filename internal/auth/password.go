package auth

import "golang.org/x/crypto/bcrypt"

const bcryptCost = 12

// HashPassword hashes a password for storage.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword compares a bcrypt hash with a plaintext password.
func CheckPassword(hashed, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password)) == nil
}
