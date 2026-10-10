package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"GoServer/internal/database"
	"GoServer/internal/handler"
	"GoServer/internal/midware"
	"GoServer/internal/response"
	"GoServer/internal/security"
	"GoServer/internal/service"

	"github.com/gin-gonic/gin"
)

func main() {
	// ========================================
	// 1. 初始化 MySQL 数据库
	// ========================================
	db, err := database.InitDB()
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("获取数据库连接池失败: %v", err)
	}
	defer sqlDB.Close()

	// ========================================
	// 2. 初始化 JWTManager
	// ========================================
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatal("JWT_SECRET 环境变量未设置")
	}

	jwtManager, err := security.NewJWTManager(
		jwtSecret,
		time.Hour,
	)
	if err != nil {
		log.Fatalf("初始化 JWTManager 失败: %v", err)
	}

	// ========================================
	// 3. 初始化 Service 和 Handler
	// ========================================
	authService := service.NewAuthService(db, jwtManager)
	authHandler := handler.NewAuthHandler(authService)

	conversationService := service.NewConversationService(db)
	conversationHandler := handler.NewConversationHandler(
		conversationService,
	)

	// ========================================
	// 4. 创建 Gin Engine
	// ========================================
	r := gin.New()

	// 不信任任意代理，适用于当前本地开发环境
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatalf("配置可信代理失败: %v", err)
	}

	// ========================================
	// 5. 注册全局 Middleware
	// ========================================
	r.Use(midware.RequestID())
	r.Use(gin.Logger())

	r.Use(gin.CustomRecovery(func(c *gin.Context, recovered any) {
		c.Abort()

		response.Error(
			c,
			http.StatusInternalServerError,
			response.ErrorCode,
			"internal server error",
		)
	}))

	r.Use(midware.Timeout(3 * time.Second))

	// ========================================
	// 6. 注册公共路由
	// ========================================
	r.GET("/healthz", handler.HandleHealthz)

	auth := r.Group("/api/v1/auth")
	{
		auth.POST("/register", authHandler.Register)
		auth.POST("/login", authHandler.Login)
	}

	// 需要 JWT 才能访问的私有路由
	private := r.Group("/api/v1")
	private.Use(midware.JWTAuth(jwtManager))
	{
		private.GET("/users/me", handler.HandleMe)

		// 新增
		private.GET(
			"/conversations/:id",
			conversationHandler.GetConversation,
		)
	}
	// ========================================
	// 7. 启动 HTTP Server
	// ========================================
	log.Println("Go-Server starting on :8080")

	if err := r.Run(":8080"); err != nil {
		log.Fatalf("HTTP 服务启动失败: %v", err)
	}
}
