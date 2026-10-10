package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"GoServer/internal/model"
	"GoServer/internal/repository"
	"GoServer/internal/security"
	"golang.org/x/crypto/bcrypt"

	"gorm.io/gorm"
)

var (
	ErrInvalidRegisterInput = errors.New("invalid registration input")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrUserAlreadyExists    = errors.New("user already exists")
)

type RegisterInput struct {
	Username string
	Email    string
	Password string
}

type RegisterResult struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type LoginInput struct {
	Email    string
	Password string
}

type LoginResult struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

type AuthService struct {
	db     *gorm.DB
	tokens *security.JWTManager
}

func NewAuthService(db *gorm.DB, tokens *security.JWTManager) *AuthService {
	return &AuthService{
		db:     db,
		tokens: tokens,
	}
}

func (s *AuthService) Register(
	ctx context.Context,
	input RegisterInput,
) (*RegisterResult, error) {
	username := strings.TrimSpace(input.Username)
	email := strings.ToLower(strings.TrimSpace(input.Email))

	//参数校验
	usernameLen := utf8.RuneCountInString(username)

	if usernameLen < 3 || usernameLen > 50 || email == "" || len(email) > 255 ||
		len(input.Password) < 8 || len(input.Password) > 72 {
		return nil, ErrInvalidRegisterInput
	}
	//生成密码哈希
	hash, err := security.HashPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password:%w", err)
	}

	//构造数据库model
	user := &model.Users{
		UserName: username,
		Email:    email,
		Password: hash,
	}

	//插入数据库
	err = repository.CreateUser(ctx, s.db, user)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, ErrUserAlreadyExists
		}
		return nil, fmt.Errorf("create user:%w", err)
	}

	//返回安全的用户信息
	return &RegisterResult{
		ID:       user.ID,
		Username: user.UserName,
		Email:    user.Email,
	}, nil
}

func (s *AuthService) Login(
	ctx context.Context,
	input LoginInput,
) (*LoginResult, error) {
	//规范化邮箱
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" || input.Password == "" ||
		len(input.Password) > 72 {
		return nil, ErrInvalidCredentials
	}

	//根据邮箱查找用户
	user, err := repository.GetUserByEmail(ctx, s.db, email)
	if err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("login lookup:%w", err)
	}

	//验证密码
	err = security.CheckPassword(user.Password, input.Password)
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return nil, ErrInvalidCredentials
		}

		return nil, fmt.Errorf("verify password: %w", err)
	}

	// 4. 密码校验通过，签发 JWT
	accessToken, err := s.tokens.Generate(user.ID)
	if err != nil {
		return nil, fmt.Errorf("generate JWT: %w", err)
	}

	// 5. 返回登录结果
	return &LoginResult{
		AccessToken: accessToken,
		TokenType:   "Bearer",
		ExpiresIn:   s.tokens.ExpiresInSeconds(),
	}, nil
}
