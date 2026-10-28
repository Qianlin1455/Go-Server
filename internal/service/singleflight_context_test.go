package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func TestSingleflight_LeaderCancellationShared(t *testing.T) {
	var group singleflight.Group

	leaderCtx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	key := "project:1:10"

	// 第一个调用注册执行任务
	leader := group.DoChan(key, func() (any, error) {
		<-leaderCtx.Done()
		return nil, leaderCtx.Err()
	})

	// 相同 Key 的第二个调用加入等待。
	// 这个函数不应该真正执行。
	follower := group.DoChan(key, func() (any, error) {
		return "unexpected", nil
	})

	// 取消 Leader 的 Context
	cancel()

	// 检查两个调用是否共享取消错误
	for i, ch := range []<-chan singleflight.Result{
		leader, follower,
	} {
		select {
		case result := <-ch:
			if !errors.Is(result.Err, context.Canceled) {
				t.Fatalf(
					"请求%d预期取消错误，实际: %v",
					i+1, result.Err,
				)
			}
			if !result.Shared {
				t.Fatalf("请求%d未共享执行结果", i+1)
			}

		case <-time.After(time.Second):
			t.Fatal("等待 Singleflight 结果超时")
		}
	}

	t.Log("PASS: Leader 取消导致共享任务失败")
}

func TestSingleflight_IndependentWorkContext(t *testing.T) {
	var group singleflight.Group

	leaderCtx, cancelLeader := context.WithCancel(
		context.Background(),
	)
	defer cancelLeader()

	workCtx, cancelWork := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancelWork()

	entered := make(chan struct{})
	release := make(chan struct{})

	// Leader 开始执行共享任务
	leader := group.DoChan("project:1:10",
		func() (any, error) {
			close(entered)

			select {
			case <-release:
				return "project-data", nil
			case <-workCtx.Done():
				return nil, workCtx.Err()
			}
		},
	)

	// 确保共享任务已经启动
	<-entered

	// Follower 加入相同 Key 的任务
	follower := group.DoChan("project:1:10",
		func() (any, error) {
			return "unexpected", nil
		},
	)

	// 取消 Leader 的请求
	cancelLeader()

	// Leader 可以停止等待
	select {
	case <-leaderCtx.Done():
		t.Log("Leader 已取消等待")
	case <-leader:
		t.Fatal("共享任务不应提前完成")
	}

	// 共享任务继续执行
	close(release)

	// Follower 仍应成功得到结果
	select {
	case result := <-follower:
		if result.Err != nil {
			t.Fatal(result.Err)
		}
		if result.Val != "project-data" {
			t.Fatalf("结果错误: %v", result.Val)
		}
		if !result.Shared {
			t.Fatal("预期共享执行结果")
		}

	case <-time.After(2 * time.Second):
		t.Fatal("Follower 等待超时")
	}

	t.Log("PASS: Leader 取消后 Follower 仍获得结果")
}
