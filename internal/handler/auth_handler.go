package handler

import (
	"GoServer/internal/service"
	"errors"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
)

type AuthHandler struct {
	authService service.AuthService
}

func NewAuthHandler(s *service.AuthService) *AuthHandler {
	return &AuthHandler{
		authService: *s,
	}
}

type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=50"`
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest

	// 1. 解析请求参数
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "登录参数不合法",
		})
		return
	}

	// 2. 调用 Service
	result, err := h.authService.Login(
		c.Request.Context(),
		service.LoginInput{
			Email:    req.Email,
			Password: req.Password,
		},
	)

	// 3. 将业务错误转换为 HTTP 响应
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidCredentials):
			c.JSON(http.StatusUnauthorized, gin.H{
				"code":    "INVALID_CREDENTIALS",
				"message": "邮箱或密码错误",
			})

		default:
			_ = c.Error(err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "服务器内部错误",
			})
		}
		return
	}

	// 4. 登录成功，返回 JWT
	c.JSON(http.StatusOK, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}

func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest

	//解析JSON并检查基本格式
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("register binding failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{
			"code":    "INVALID_ARGUMENT",
			"message": "注册参数不合法",
		})
		return
	}

	//调用service层
	result, err := h.authService.Register(
		c.Request.Context(),
		service.RegisterInput{
			Username: req.Username,
			Email:    req.Email,
			Password: req.Password,
		},
	)

	//错误处理
	if err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidRegisterInput):
			c.JSON(http.StatusBadRequest, gin.H{
				"code":    "INVALID_ARGUMENT",
				"message": "用户名、邮箱或密码不符合要求",
			})
		case errors.Is(err, service.ErrUserAlreadyExists):
			c.JSON(http.StatusConflict, gin.H{
				"code":    "USER_ALREADY_EXISTS",
				"message": "用户名或邮箱已存在",
			})
		default:
			c.Error(err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    "INTERNAL_ERROR",
				"message": "内部错误",
			})
		}
		return
	}
	//注册成功
	c.JSON(http.StatusCreated, gin.H{
		"code": "SUCCESS",
		"data": result,
	})
}
