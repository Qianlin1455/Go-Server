package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"GoServer/internal/consumer"
)

func main() {
	//创建支持优雅退出的Context
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	//读取broker
	brokers := []string{"127.0.0.1:9092"}
	if raw := os.Getenv("KAFKA_BROKERS"); raw != "" {
		brokers = strings.Split(raw, ",")
	}

	//Consumer GroupID
	groupID := consumer.DefaultGroupID
	if raw := os.Getenv("KAFKA_GROUP_ID"); raw != "" {
		groupID = raw
	}

	//创建handler
	handler := consumer.NewHandler()
	//创建Consumer
	c, err := consumer.NewConsumer(
		brokers, groupID, handler,
	)
	if err != nil {
		log.Fatalf("create consumer:%v", err)
	}
	log.Printf(
		"starting kafka consumer:brokers=%v group=%s",
		brokers, groupID,
	)

	//启动消费
	if err := c.Run(ctx); err != nil {
		log.Fatalf("consumer stopped:%v", err)
	}
}
