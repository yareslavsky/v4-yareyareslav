package auth

type UserID string

type User struct {
	ID           *UserID
	Email        string
	PasswordHash string
	Role         string
}

type JWT struct {
	AccessToken  string
	RefreshToken string
}
