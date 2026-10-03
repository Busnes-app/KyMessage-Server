package auth

import (
	"errors"
	"regexp"
	"strings"

	"github.com/Busnes-app/ky_server_base/internal/config"
)

var (
	ErrPasswordTooShort = errors.New("password must be at least 12 characters")
	ErrInvalidUsername  = errors.New("username must be 3-64 alphanumeric characters, underscores, or hyphens")
	ErrInvalidEmail     = errors.New("invalid email address format")
	emailRegex          = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
)

// ValidatePassword ensures password satisfies standard KySecurity length policy (>= 12 chars).
func ValidatePassword(password string) error {
	if len(strings.TrimSpace(password)) < 12 {
		return ErrPasswordTooShort
	}
	return nil
}

// ValidateUsername checks username syntax.
func ValidateUsername(username string) error {
	if !config.ValidUsername(username) {
		return ErrInvalidUsername
	}
	return nil
}

// ValidateEmail checks email syntax if provided.
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if !emailRegex.MatchString(email) {
		return ErrInvalidEmail
	}
	return nil
}
