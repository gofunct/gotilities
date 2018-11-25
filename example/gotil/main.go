package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/gofunct/gotilities"
	pb "github.com/gofunct/gotilities/proto/ping"
	"go.uber.org/zap"
	"os"
	"strings"
	"time"
)

func main() {

	var gotility gotilities.Gotility

	logger, err := gotility.MakeZapper(true)

	gotility.ZapErr(logger, "failed to create logger", err)

	defer logger.Sync()

	tracer, closer, err := gotility.MakeTracer("demo", logger)

	gotility.ZapErr(logger, "failed to create tracer", err)

	defer closer.Close()

	metrics := gotility.RegGrpcServerMetrics(false)

	conn, err := gotility.MakeGrpcClient("8080", tracer, metrics)
	gotility.ZapErr(logger, "failed to create grpc dialer", err)

	defer conn.Close()

	client := pb.NewDemoServiceClient(conn)

	go func() {
		for {
			// Call “SayHello” method and wait for response from gRPC Server.
			_, err := client.SayHello(context.Background(), &pb.HelloRequest{Name: "Test"})
			if err != nil {
				logger.Info("Calling the SayHello method unsuccessfully. ErrorInfo: %+v", zap.Error(err))
				logger.Debug("You should stop the process")
				return
			}
			time.Sleep(3 * time.Second)
		}
	}()
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("You can press n or N to stop the process of client")
	for scanner.Scan() {
		if strings.ToLower(scanner.Text()) == "n" {
			os.Exit(0)
		}
	}
}
