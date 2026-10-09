package response

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	SuccessCode = 0
	ErrorCode   = -1
)

type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func Success(c *gin.Context, data any) {
	rep := Response{
		Code:    SuccessCode,
		Message: "ok",
		Data:    data,
	}
	c.JSON(http.StatusOK, rep)
}

func Error(c *gin.Context, httpStatus int, code int, message string) {
	rep := Response{
		Code:    code,
		Message: message,
		Data:    nil,
	}

	c.JSON(httpStatus, rep)
}
