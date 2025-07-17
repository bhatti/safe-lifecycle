package main

import (
	"context"
	"fmt"
	"github.com/bhatti/safe-lifecycle/api"
	"log"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: go run client/main.go <server-address>")
	}
	addr := os.Args[1]

	log.Printf("Starting client, connecting to %s", addr)

	// Set up a connection to the server.
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithBlock())
	if err != nil {
		log.Fatalf("did not connect: %v", err)
	}
	defer conn.Close()
	c := api.NewDemoServiceClient(conn)

	// Contact the server in a loop.
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	requestCount := 0

	for range ticker.C {
		requestCount++
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)

		req := &api.WorkRequest{Data: fmt.Sprintf("request-%d", requestCount)}
		_, err = c.DoWork(ctx, req)

		if err != nil {
			log.Printf("❌ Request failed: %v", err)
		} else {
			log.Printf("✅ Request successful")
		}
		cancel()
	}
}
