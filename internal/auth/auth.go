package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// HashPassword returns the SHA-256 hex digest of password+salt.
func HashPassword(password, salt string) string {
	h := sha256.Sum256([]byte(password + salt))
	return hex.EncodeToString(h[:])
}

// CheckPassword returns true when the hash matches SHA-256(password+salt).
func CheckPassword(password, salt, hash string) bool {
	return HashPassword(password, salt) == hash
}

// GenerateToken creates a signed JWT valid for 24 hours.
// Claims: sub = userID, role = role.
func GenerateToken(userID, role, jwtSecret string) (string, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":  userID,
		"role": role,
		"iat":  now.Unix(),
		"exp":  now.Add(24 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(jwtSecret))
}

// ParseToken validates a JWT string and extracts userID and role.
func ParseToken(tokenStr, jwtSecret string) (userID string, role string, err error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	})
	if err != nil {
		return "", "", fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", "", fmt.Errorf("invalid token claims")
	}

	sub, _ := claims["sub"].(string)
	r, _ := claims["role"].(string)
	if sub == "" {
		return "", "", fmt.Errorf("missing sub claim")
	}
	return sub, r, nil
}

// GenerateSalt returns 16 random hex characters (8 random bytes).
func GenerateSalt() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
