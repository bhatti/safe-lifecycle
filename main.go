//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative  api/demo.proto

package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/bhatti/safe-lifecycle/api"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	health "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// DemoServiceServer now implements the generated interface.
type DemoServiceServer struct {
	api.UnimplementedDemoServiceServer
}

func (s *DemoServiceServer) DoWork(ctx context.Context, req *api.WorkRequest) (*api.WorkResponse, error) {
	log.Printf("Received work request: %s", req.GetData())
	// Simulate doing some work that could be interrupted
	time.Sleep(200 * time.Millisecond)
	return &api.WorkResponse{Result: "Work completed successfully for: " + req.GetData()}, nil
}

// ... (The 'state' struct and 'HealthChecker' struct remain exactly the same as before) ...
// state represents the internal state of our application.
type state struct {
	dbHealthy         atomic.Bool
	downstreamHealthy atomic.Bool
	isInitialized     atomic.Bool
	shutdownInitiated atomic.Bool
}

// HealthChecker implements the grpc.health.v1.HealthServer interface.
type HealthChecker struct {
	appState *state
}

// Check implements the health check logic.
func (h *HealthChecker) Check(ctx context.Context, req *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	// Liveness check is always simple and internal.
	// It just checks if the process is running and not in a shutdown sequence.
	if req.Service == "" || req.Service == "liveness" {
		if h.appState.shutdownInitiated.Load() {
			log.Println("Liveness check failed: shutdown in progress")
			return &health.HealthCheckResponse{Status: health.HealthCheckResponse_NOT_SERVING}, nil
		}
		return &health.HealthCheckResponse{Status: health.HealthCheckResponse_SERVING}, nil
	}

	// Readiness and Startup probes check all dependencies.
	if req.Service == "readiness" || req.Service == "startup" {
		// 1. Check if one-time initialization is complete.
		if !h.appState.isInitialized.Load() {
			log.Println("Readiness/Startup check failed: not initialized yet")
			return &health.HealthCheckResponse{Status: health.HealthCheckResponse_NOT_SERVING}, nil
		}
		// 2. Check "database" dependency.
		if !h.appState.dbHealthy.Load() {
			log.Println("Readiness/Startup check failed: database is not healthy")
			return &health.HealthCheckResponse{Status: health.HealthCheckResponse_NOT_SERVING}, nil
		}
		// 3. Check "downstream service" dependency.
		if !h.appState.downstreamHealthy.Load() {
			log.Println("Readiness/Startup check failed: downstream service is not healthy")
			return &health.HealthCheckResponse{Status: health.HealthCheckResponse_NOT_SERVING}, nil
		}
		// 4. Check if a shutdown has been initiated.
		if h.appState.shutdownInitiated.Load() {
			log.Println("Readiness/Startup check failed: shutdown in progress")
			return &health.HealthCheckResponse{Status: health.HealthCheckResponse_NOT_SERVING}, nil
		}
		// All checks passed.
		return &health.HealthCheckResponse{Status: health.HealthCheckResponse_SERVING}, nil
	}
	return nil, status.Errorf(codes.NotFound, "unknown service: %s", req.Service)
}

// Watch is not implemented.
func (h *HealthChecker) Watch(req *health.HealthCheckRequest, server health.Health_WatchServer) error {
	return status.Error(codes.Unimplemented, "Watch is not implemented.")
}

func (h *HealthChecker) List(ctx context.Context, request *health.HealthListRequest) (*health.HealthListResponse, error) {
	return nil, status.Error(codes.Unimplemented, "List is not implemented.")
}

func main() {
	var startupDelay = flag.Duration("startup-delay", 0*time.Second, "Time to wait before marking the service as initialized.")
	flag.Parse()

	log.Printf("Application starting... configured startup delay is %s.", *startupDelay)

	// --- Initialize State ---
	appState := &state{}
	appState.dbHealthy.Store(true)         // Start healthy
	appState.downstreamHealthy.Store(true) // Start healthy
	appState.isInitialized.Store(false)    // Start uninitialized

	// --- Management HTTP Server ---
	// This server runs on a separate port to allow us to toggle health states.
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/toggle-db-health", func(w http.ResponseWriter, r *http.Request) {
			current := appState.dbHealthy.Load()
			appState.dbHealthy.Store(!current)
			log.Printf("Toggled DB health to: %t\n", !current)
			fmt.Fprintf(w, "DB health is now: %t\n", !current)
		})
		mux.HandleFunc("/toggle-downstream-health", func(w http.ResponseWriter, r *http.Request) {
			current := appState.downstreamHealthy.Load()
			appState.downstreamHealthy.Store(!current)
			log.Printf("Toggled Downstream Service health to: %t\n", !current)
			fmt.Fprintf(w, "Downstream Service health is now: %t\n", !current)
		})
		if err := http.ListenAndServe(":9090", mux); err != nil {
			log.Fatalf("Failed to start management server: %v", err)
		}
	}()

	// --- gRPC Server ---
	lis, err := net.Listen("tcp", ":8085")
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}
	grpcServer := grpc.NewServer()

	// Register our services using the generated function
	api.RegisterDemoServiceServer(grpcServer, &DemoServiceServer{})
	health.RegisterHealthServer(grpcServer, &HealthChecker{appState: appState})

	// Start serving gRPC in a separate goroutine
	go func() {
		log.Println("gRPC server listening on :8085")
		if err := grpcServer.Serve(lis); err != nil {
			if err != grpc.ErrServerStopped {
				log.Fatalf("gRPC server failed: %v", err)
			}
		}
	}()

	// --- Handle Slow Startup ---
	if *startupDelay > 0 {
		log.Printf("Simulating slow startup for %s...", *startupDelay)
		time.Sleep(*startupDelay)
	}
	appState.isInitialized.Store(true)
	log.Println("Service initialization complete. Ready to serve traffic.")

	// --- Graceful Shutdown Handling ---
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	sig := <-stop
	log.Printf("Received signal '%s', initiating graceful shutdown...", sig)
	appState.shutdownInitiated.Store(true)

	grpcServer.GracefulStop()

	log.Println("gRPC server stopped gracefully. Application exiting.")
}
