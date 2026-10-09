package handler

import (
	"GoServer/internal/response"
	"github.com/gin-gonic/gin"
)

type data struct {
	Status string `json:"status"`
}

func HandleHealthz(c *gin.Context) {
	response.Success(c, data{
		Status: "ok",
	})
}
