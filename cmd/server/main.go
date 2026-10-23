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
	// 1. 读取第一次快照
	previous := sqlDB.Stats()

	// 2. 创建退出信号
	stopStats := make(chan struct{})
	defer close(stopStats)

	// 3. 启动监控 goroutine
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// 4. 读取当前快照
				current := sqlDB.Stats()

				// 5. 计算增量
				waitCountDelta :=
					current.WaitCount - previous.WaitCount

				waitDurationDelta :=
					current.WaitDuration - previous.WaitDuration

				var avgWait time.Duration

				if waitCountDelta > 0 {
					avgWait = waitDurationDelta /
						time.Duration(waitCountDelta)
				}

				// 6. 打印统计结果
				log.Printf(
					"DB Pool: Open=%d InUse=%d Idle=%d "+
						"WaitCount=%d Delta=%d "+
						"WaitDuration=%s Delta=%s AvgWait=%s",
					current.OpenConnections,
					current.InUse,
					current.Idle,
					current.WaitCount,
					waitCountDelta,
					current.WaitDuration,
					waitDurationDelta,
					avgWait,
				)

				// 7. 更新上一轮快照
				previous = current

			case <-stopStats:
				return
			}
		}
	}()

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

	projectService := service.NewProjectService(db)
	projectHandler := handler.NewProjectHandler(projectService)

	conversationService := service.NewConversationService(db)
	conversationHandler := handler.NewConversationHandler(
		conversationService,
	)
	messageService := service.NewMessageService(db)
	messageHandler := handler.NewMessageHandler(
		messageService,
	)

	taskService := service.NewTaskService(db)
	taskHandler := handler.NewTaskHandler(taskService)

	evidenceService := service.NewEvidenceService(db)
	evidenceHandler := handler.NewEvidenceHandler(
		evidenceService,
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
		private.POST(
			"/conversations",
			conversationHandler.CreateConversation,
		)
		// 新增
		private.GET(
			"/conversations/:id",
			conversationHandler.GetConversation,
		)
		private.POST(
			"/conversations/:id/messages",
			messageHandler.CreateMessage,
		)

		private.GET(
			"/conversations/:id/messages",
			messageHandler.ListMessages,
		)
		private.POST("/projects", projectHandler.CreateProject)
		private.GET("/projects/:id", projectHandler.GetProject)
		private.GET("/projects", projectHandler.ListProjects)
		private.GET(
			"/projects/:id/conversations",
			conversationHandler.ListProjectConversations,
		)
		// Task API
		private.GET("/tasks/:id/retrospective", taskHandler.Retrospective)
		private.POST(
			"/projects/:id/tasks",
			taskHandler.CreateTask,
		)

		private.GET(
			"/projects/:id/tasks",
			taskHandler.ListTasks,
		)

		private.PATCH(
			"/tasks/:id/status",
			taskHandler.ChangeStatus,
		)
		private.POST(
			"/tasks/:id/evidence",
			evidenceHandler.CreateEvidence,
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
