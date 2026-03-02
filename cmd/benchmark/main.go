package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/hiddify/hue-go/internal/domain"
	"github.com/hiddify/hue-go/internal/engine"
	"github.com/hiddify/hue-go/internal/storage/cache"
	"github.com/hiddify/hue-go/internal/storage/sqlite"
	"go.uber.org/zap"
)

type benchmarkScenario struct {
	Name     string
	Users    int
	Duration time.Duration
	Interval time.Duration
}

type benchmarkResult struct {
	Scenario      benchmarkScenario
	ActualTime    time.Duration
	TotalRequests int64
	TotalErrors   int64
	TotalRejected int64
	AvgRPS        float64
	PeakAllocMB   uint64
	PeakSysMB     uint64
	PeakGoroutine int
}

func main() {
	usersFlag := flag.Int("users", 1000, "Number of users to simulate (single mode)")
	durationFlag := flag.Duration("duration", 5*time.Minute, "Duration of benchmark run")
	intervalFlag := flag.Duration("interval", 1*time.Second, "Interval between reports per user")
	suiteFlag := flag.Bool("suite", false, "Run the built-in 5-case mini benchmark suite")
	flag.Parse()

	if *suiteFlag {
		runMiniSuite()
		return
	}

	scenario := benchmarkScenario{
		Name:     "single",
		Users:    *usersFlag,
		Duration: *durationFlag,
		Interval: *intervalFlag,
	}

	result, err := runScenario(scenario, true)
	if err != nil {
		log.Fatalf("Benchmark failed: %v", err)
	}

	printScenarioSummary(result)
}

func runMiniSuite() {
	scenarios := []benchmarkScenario{
		{Name: "mini-1", Users: 100, Duration: 45 * time.Second, Interval: 1 * time.Second},
		{Name: "mini-2", Users: 1000, Duration: 45 * time.Second, Interval: 1 * time.Second},
		{Name: "mini-3", Users: 10000, Duration: 45 * time.Second, Interval: 2 * time.Second},
		{Name: "mini-4", Users: 1000, Duration: 60 * time.Second, Interval: 500 * time.Millisecond},
		{Name: "mini-5", Users: 10000, Duration: 60 * time.Second, Interval: 1 * time.Second},
	}

	fmt.Println("Running 5 mini benchmarks (real simulation mode)...")
	results := make([]benchmarkResult, 0, len(scenarios))

	for _, scenario := range scenarios {
		fmt.Printf("\n=== %s | users=%d duration=%v interval=%v ===\n", scenario.Name, scenario.Users, scenario.Duration, scenario.Interval)

		result, err := runScenario(scenario, false)
		if err != nil {
			fmt.Printf("Scenario %s failed: %v\n", scenario.Name, err)
			continue
		}
		results = append(results, result)
		printScenarioSummary(result)
	}

	if len(results) == 0 {
		fmt.Println("No scenario completed successfully.")
		return
	}

	fmt.Println("\n=== Mini Suite Summary ===")
	fmt.Println("Scenario | Users | Duration | Requests | Errors | Rejected | Avg RPS | PeakAllocMB | PeakSysMB | PeakG")
	for _, r := range results {
		fmt.Printf("%s | %d | %v | %d | %d | %d | %.2f | %d | %d | %d\n",
			r.Scenario.Name,
			r.Scenario.Users,
			r.ActualTime.Truncate(time.Millisecond),
			r.TotalRequests,
			r.TotalErrors,
			r.TotalRejected,
			r.AvgRPS,
			r.PeakAllocMB,
			r.PeakSysMB,
			r.PeakGoroutine,
		)
	}
}

func runScenario(scenario benchmarkScenario, showLiveMetrics bool) (benchmarkResult, error) {
	fmt.Printf("Starting benchmark with %d users for %v (interval: %v)\n", scenario.Users, scenario.Duration, scenario.Interval)

	logger, err := zap.NewProduction()
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("create logger: %w", err)
	}
	defer logger.Sync()

	dbBase := fmt.Sprintf("benchmark_%s_%d.db", scenario.Name, time.Now().UnixNano())
	dbPath := "sqlite://" + dbBase
	defer cleanupDBFiles(dbBase)

	userDB, err := sqlite.NewUserDB(dbPath)
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("create user DB: %w", err)
	}
	defer userDB.Close()

	if err := userDB.Migrate(); err != nil {
		return benchmarkResult{}, fmt.Errorf("migrate user DB: %w", err)
	}

	activeDB, err := sqlite.NewActiveDB(dbPath)
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("create active DB: %w", err)
	}
	defer activeDB.Close()

	memCache := cache.NewMemoryCache()
	quotaEngine := engine.NewQuotaEngine(userDB, activeDB, memCache, logger)
	sessionManager := engine.NewSessionManager(memCache, 5*time.Minute, logger)
	penaltyHandler := engine.NewPenaltyHandler(memCache, 1*time.Minute, logger)

	nodeID := uuid.New().String()
	err = userDB.CreateNode(&domain.Node{
		ID:                nodeID,
		Name:              "Benchmark Node",
		TrafficMultiplier: 1.0,
		ResetMode:         domain.ResetModeNoReset,
	})
	if err != nil {
		return benchmarkResult{}, fmt.Errorf("create node: %w", err)
	}

	fmt.Println("Provisioning users and packages...")
	userIDs := make([]string, scenario.Users)
	for i := 0; i < scenario.Users; i++ {
		userID := uuid.New().String()
		pkgID := uuid.New().String()
		userIDs[i] = userID

		err = userDB.CreateUser(&domain.User{
			ID:              userID,
			Username:        fmt.Sprintf("user_%d", i),
			Status:          domain.UserStatusActive,
			ActivePackageID: &pkgID,
		})
		if err != nil {
			return benchmarkResult{}, fmt.Errorf("create user: %w", err)
		}

		err = userDB.CreatePackage(&domain.Package{
			ID:            pkgID,
			UserID:        userID,
			TotalTraffic:  1000 * 1024 * 1024 * 1024,
			MaxConcurrent: 5,
			Status:        domain.PackageStatusActive,
		})
		if err != nil {
			return benchmarkResult{}, fmt.Errorf("create package: %w", err)
		}
	}
	fmt.Println("Provisioning complete.")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = activeDB.Flush()
			}
		}
	}()

	var wg sync.WaitGroup
	var totalRequests int64
	var totalErrors int64
	var totalRejected int64
	var peakAllocMB uint64
	var peakSysMB uint64
	var peakGoroutine int64

	startTime := time.Now()
	endTime := startTime.Add(scenario.Duration)

	fmt.Println("Starting simulation...")

	for i := 0; i < scenario.Users; i++ {
		wg.Add(1)
		go func(uID string, index int) {
			defer wg.Done()

			time.Sleep(time.Duration(rand.Int63n(int64(scenario.Interval))))

			sessionID := uuid.New().String()
			clientIP := fmt.Sprintf("192.168.%d.%d", (index/250)%255, index%250)

			ticker := time.NewTicker(scenario.Interval)
			defer ticker.Stop()

			for time.Now().Before(endTime) {
				upload := rand.Int63n(1024 * 1024)
				download := rand.Int63n(5 * 1024 * 1024)

				penaltyResult := penaltyHandler.CheckPenalty(uID)
				if !penaltyResult.HasPenalty {
					sessionResult := sessionManager.CheckSession(uID, sessionID, clientIP, 5)
					if sessionResult.SessionLimitHit {
						penaltyHandler.ApplyPenalty(uID, "concurrent_session_limit_exceeded")
					} else {
						quotaResult, quotaErr := quotaEngine.CheckQuota(uID, upload, download)
						if quotaErr != nil {
							atomic.AddInt64(&totalErrors, 1)
						} else if !quotaResult.CanUse {
							atomic.AddInt64(&totalRejected, 1)
						} else {
							recordErr := quotaEngine.RecordUsage(uID, upload, download)
							if recordErr != nil {
								atomic.AddInt64(&totalErrors, 1)
							}
						}
					}
				}

				atomic.AddInt64(&totalRequests, 1)
				<-ticker.C
			}
		}(userIDs[i], i)
	}

	monitorDone := make(chan struct{})
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	defer cancelMonitor()
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		var m runtime.MemStats

		for {
			select {
			case <-monitorCtx.Done():
				close(monitorDone)
				return
			case <-ticker.C:
			}

			runtime.ReadMemStats(&m)
			reqs := atomic.LoadInt64(&totalRequests)
			errs := atomic.LoadInt64(&totalErrors)
			rejected := atomic.LoadInt64(&totalRejected)
			elapsed := time.Since(startTime).Seconds()
			rps := float64(reqs) / elapsed

			allocMB := m.Alloc / 1024 / 1024
			sysMB := m.Sys / 1024 / 1024
			goroutines := int64(runtime.NumGoroutine())

			for {
				cur := atomic.LoadUint64(&peakAllocMB)
				if allocMB <= cur || atomic.CompareAndSwapUint64(&peakAllocMB, cur, allocMB) {
					break
				}
			}
			for {
				cur := atomic.LoadUint64(&peakSysMB)
				if sysMB <= cur || atomic.CompareAndSwapUint64(&peakSysMB, cur, sysMB) {
					break
				}
			}
			for {
				cur := atomic.LoadInt64(&peakGoroutine)
				if goroutines <= cur || atomic.CompareAndSwapInt64(&peakGoroutine, cur, goroutines) {
					break
				}
			}

			if showLiveMetrics {
				fmt.Printf("[%.0fs] Reqs: %d (%.2f req/s) | Errs: %d | Rejected: %d | Alloc: %d MB | Sys: %d MB | G: %d\n",
					elapsed, reqs, rps, errs, rejected, allocMB, sysMB, goroutines)
			}
		}
	}()

	wg.Wait()
	var finalMem runtime.MemStats
	runtime.ReadMemStats(&finalMem)
	finalAllocMB := finalMem.Alloc / 1024 / 1024
	finalSysMB := finalMem.Sys / 1024 / 1024
	finalGoroutines := int64(runtime.NumGoroutine())

	if finalAllocMB > atomic.LoadUint64(&peakAllocMB) {
		atomic.StoreUint64(&peakAllocMB, finalAllocMB)
	}
	if finalSysMB > atomic.LoadUint64(&peakSysMB) {
		atomic.StoreUint64(&peakSysMB, finalSysMB)
	}
	if finalGoroutines > atomic.LoadInt64(&peakGoroutine) {
		atomic.StoreInt64(&peakGoroutine, finalGoroutines)
	}

	cancelMonitor()
	_ = activeDB.Flush()
	<-monitorDone

	actualDuration := time.Since(startTime)
	finalReqs := atomic.LoadInt64(&totalRequests)
	finalErrs := atomic.LoadInt64(&totalErrors)
	finalRejected := atomic.LoadInt64(&totalRejected)

	result := benchmarkResult{
		Scenario:      scenario,
		ActualTime:    actualDuration,
		TotalRequests: finalReqs,
		TotalErrors:   finalErrs,
		TotalRejected: finalRejected,
		AvgRPS:        float64(finalReqs) / actualDuration.Seconds(),
		PeakAllocMB:   atomic.LoadUint64(&peakAllocMB),
		PeakSysMB:     atomic.LoadUint64(&peakSysMB),
		PeakGoroutine: int(atomic.LoadInt64(&peakGoroutine)),
	}

	return result, nil
}

func printScenarioSummary(result benchmarkResult) {
	fmt.Println("\n--- Benchmark Results ---")
	fmt.Printf("Scenario: %s\n", result.Scenario.Name)
	fmt.Printf("Total Users: %d\n", result.Scenario.Users)
	fmt.Printf("Duration: %v\n", result.ActualTime.Truncate(time.Millisecond))
	fmt.Printf("Total Requests: %d\n", result.TotalRequests)
	fmt.Printf("Total Errors: %d\n", result.TotalErrors)
	fmt.Printf("Total Rejected: %d\n", result.TotalRejected)
	fmt.Printf("Average RPS: %.2f\n", result.AvgRPS)
	fmt.Printf("Peak Alloc: %d MB\n", result.PeakAllocMB)
	fmt.Printf("Peak Sys: %d MB\n", result.PeakSysMB)
	fmt.Printf("Peak Goroutines: %d\n", result.PeakGoroutine)
}

func cleanupDBFiles(base string) {
	_ = os.Remove(base)
	_ = os.Remove(base[:len(base)-3] + "_active.db")
}
