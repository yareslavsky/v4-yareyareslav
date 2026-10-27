package auth

func JWTToLoginUserResponse(jwt JWT) LoginUserResponse {
	return LoginUserResponse(jwt)
}
