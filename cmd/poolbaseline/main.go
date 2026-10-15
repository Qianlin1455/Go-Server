package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"GoServer/internal/database"
)

const (
	totalRequests = 100
	queryTimeout  = 3 * time.Second
)

// sample records database time only, after passing the client-side
// concurrency gate. It includes waiting for a free database connection.
type sample struct {
	duration time.Duration
	err      error
}

func percentile(values []time.Duration, pct int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	index := (pct*len(values)+99)/100 - 1
	if index < 0 {
		index = 0
	}
	return values[index]
}

func main() {
	db, err := database.InitDB()
	if err != nil {
		log.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatal(err)
	}
	defer sqlDB.Close()

	fmt.Println("DB pool baseline: 100 requests per scenario, SELECT SLEEP(0.02)")
	fmt.Println("Run against a LOCAL TEST database, not production.")
	fmt.Println("Latency excludes the client-side concurrency gate.")

	for _, maxOpen := range []int{2, 10} {
		sqlDB.SetMaxOpenConns(maxOpen)
		sqlDB.SetMaxIdleConns(2)

		for _, concurrency := range []int{1, 10, 50} {
			before := sqlDB.Stats()
			gate := make(chan struct{}, concurrency)
			results := make(chan sample, totalRequests)
			var wg sync.WaitGroup

			start := time.Now()
			for i := 0; i < totalRequests; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					gate <- struct{}{}
					defer func() { <-gate }()

					ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
					defer cancel()

					begin := time.Now()
					var slept int
					err := sqlDB.QueryRowContext(ctx, "SELECT SLEEP(0.02)").Scan(&slept)
					results <- sample{duration: time.Since(begin), err: err}
				}()
			}
			wg.Wait()
			wall := time.Since(start)
			close(results)

			var latencies []time.Duration
			var failures int
			for result := range results {
				latencies = append(latencies, result.duration)
				if result.err != nil {
					failures++
					log.Printf("query failed: %v", result.err)
				}
			}
			sort.Slice(latencies, func(i, j int) bool {
				return latencies[i] < latencies[j]
			})

			after := sqlDB.Stats()
			waitCount := after.WaitCount - before.WaitCount
			waitDuration := after.WaitDuration - before.WaitDuration
			var avgWait time.Duration
			if waitCount > 0 {
				avgWait = waitDuration / time.Duration(waitCount)
			}

			fmt.Printf(
				"MaxOpen=%-2d concurrency=%-2d total=%d failures=%d "+
					"wall=%v QPS=%.1f p50=%v p95=%v "+
					"Open=%d InUse=%d Idle=%d WaitCountDelta=%d "+
					"WaitDurationDelta=%v avgWaitPerEvent=%v\n",
				maxOpen, concurrency, totalRequests, failures,
				wall.Round(time.Millisecond),
				float64(totalRequests)/wall.Seconds(),
				percentile(latencies, 50).Round(time.Millisecond),
				percentile(latencies, 95).Round(time.Millisecond),
				after.OpenConnections, after.InUse, after.Idle,
				waitCount, waitDuration.Round(time.Millisecond),
				avgWait.Round(time.Millisecond),
			)
		}
	}
}
