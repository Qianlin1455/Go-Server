package handler

import (
	"errors"
	"net/http"

	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func writeServiceerror(c *gin.Context, err error) {
	var status int
	var code, message string

	switch {
	case errors.Is(err, service.ErrInvalidMessageInput),
		errors.Is(err, service.ErrInvalidRegisterInput):
		status = http.StatusBadRequest
		code = "INVALID_ARGUMENT"
		message = "请求参数不合法"

	case errors.Is(err, service.ErrInvalidCredentials):
		status = http.StatusUnauthorized
		code = "INVALID_CREDENTIALS"
		message = "邮箱或密码错误"

	case errors.Is(err, service.ErrUserAlreadyExists):
		status = http.StatusConflict
		code = "USER_ALREADY_EXISTS"
		message = "用户已存在"

	default:
		//保留内部错误
		_ = c.Error(err)
		status = http.StatusInternalServerError
		code = "INTERNAL_ERROR"
		message = "服务器内部错误"
	}

	c.JSON(status, gin.H{
		"code":   code,
		"mesage": message,
	})
}
