package gotilities

import (
	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go/config"
	jzap "github.com/uber/jaeger-client-go/log/zap"
	jprom "github.com/uber/jaeger-lib/metrics/prometheus"
	"go.uber.org/zap"
	"io"
)

func (g *Gotility) MakeTracer(name string, logger *zap.Logger) (opentracing.Tracer, io.Closer, error) {
	jfactory := jprom.New()
	cfg, err := config.FromEnv()

	if err != nil {
		return nil, nil, err
	}

	cfg.Sampler.Type = "const"
	cfg.Sampler.Param = 1
	cfg.ServiceName = name
	cfg.RPCMetrics = true

	tracer, closer, err := cfg.NewTracer(
		config.Logger(jzap.NewLogger(logger)), //Uses a zap logger instead of the standard logger
		config.Metrics(jfactory),
	)
	return tracer, closer, err
}
