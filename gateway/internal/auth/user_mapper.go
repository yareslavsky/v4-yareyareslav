package auth

func RegisterUserRequestToUser(req RegisterUserRequest, passwordHash string) User {
	return User{
		ID:           nil,
		Email:        req.Email,
		PasswordHash: passwordHash,
		Role:         req.Role,
	}
}

func UserToRegisterUserResponse(user User, accessToken, refreshToken string) RegisterUserResponse {
	return RegisterUserResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		User: UserResponse{
			Email: user.Email,
			Role:  user.Role,
		},
	}
}

func UserToLoginUserResponse(user User, accessToken, refreshToken string) LoginUserResponse {
	return LoginUserResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}
}
