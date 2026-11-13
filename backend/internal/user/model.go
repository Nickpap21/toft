package user

import "time"

// User represents a platform administrator.
type User struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash string     `json:"-"` // Never serialize
	Role         string     `json:"role"`
	IsActive     bool       `json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	// Theme is the Settings → Appearance choice; nil until one is made.
	Theme     *string   `json:"theme"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Themes are the theme preferences a user can save: the frontend's
// THEME_PREFS, and users.theme's CHECK.
var Themes = map[string]bool{
	"system:graphite-paper": true, "system:mono-daylight": true,
	"graphite": true, "paper": true, "mono": true, "daylight": true, "ink": true, "contrast": true,
}

// Profile is the API view of the authenticated user, returned by login,
// register and /me. It never carries the password hash.
type Profile struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	// Role is users.role, kept for API compatibility. It is 'admin' on every
	// row and grants nothing; org roles and IsOperator are what count.
	Role     string `json:"role"`
	IsActive bool   `json:"is_active"`
	// IsOperator is true for instance operators (D-14, D-21), who can create
	// workspaces and manage operators.
	IsOperator  bool       `json:"is_operator"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	// Theme is the saved Settings → Appearance choice; null until one is
	// made, when the browser's own choice applies.
	Theme     *string   `json:"theme"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewProfile builds the API view of u.
func NewProfile(u *User, isOperator bool) Profile {
	return Profile{
		ID:          u.ID,
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		IsActive:    u.IsActive,
		IsOperator:  isOperator,
		LastLoginAt: u.LastLoginAt,
		Theme:       u.Theme,
		CreatedAt:   u.CreatedAt,
		UpdatedAt:   u.UpdatedAt,
	}
}

// Identity is what every authenticated request needs to know about its
// user, read in one query (middleware.UserStatusChecker).
type Identity struct {
	Active bool
	// TokensValidFrom: a session token issued before it is refused. A
	// password change moves it.
	TokensValidFrom time.Time
	// OrgID and Role are the workspace the request works in (the one it
	// named, else the one the account last switched to) and the user's role
	// in it; both "" when there is none or the user is not a member of it.
	OrgID string
	Role  string
	// SessionRevoked: the session token the request carries was signed out.
	SessionRevoked bool
}
