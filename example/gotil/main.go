package main

import (
	"bufio"
	"context"
	"fmt"
	"github.com/gofunct/gotilities/gotility"
	pb "github.com/gofunct/gotilities/proto/ping"
	"github.com/mwitkow/go-conntrack"
	"go.uber.org/zap"
	"net/http"
	"os"
	"strings"
	"time"
	"net"
)

func main() {

	var g gotility.Gotility

	logger, err := g.MakeZapper(true)

	g.ZapErr(logger, "failed to create logger", err)

	defer logger.Sync()

	tracer, closer, err := g.MakeTracer("demo", logger)

	g.ZapErr(logger, "failed to create tracer", err)

	defer closer.Close()

	metrics := g.RegGrpcServerMetrics(false)

	conn, err := g.MakeGrpcClient("8080", tracer, metrics)
	g.ZapErr(logger, "failed to create grpc dialer", err)

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


func init() {
	http.DefaultTransport.(*http.Transport).DialContext = conntrack.NewDialContextFunc(
		conntrack.DialWithTracing(),
		conntrack.DialWithDialer(&net.Dialer{
			Timeout:   30,
			KeepAlive: 30,
		}),
	)

	conntrack.PreRegisterDialerMetrics("default")
}
