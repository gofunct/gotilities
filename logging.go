package gotilities

import (
	"context"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc/metadata"
	"log"
)

func (opts *Gotility) MakeZapper() (*zap.Logger, error) {
	logger, err := zap.NewDevelopment()
	if err != nil {
		log.Fatalf("failed to create zap logger: %v", zap.Error(err))
	}

	zap.ReplaceGlobals(logger)

	logger.Debug("zap logging enabled")

	return logger, err
}

func (opts *Gotility) ZapMetaData(ctx context.Context, log *zap.Logger, fields ...zapcore.Field) *zap.Logger {
	l := log.With(fields...)
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if rid, ok := md["request_id"]; ok && len(rid) >= 1 {
			l = l.With(zap.String("request_id", rid[0]))
		}
	}
	return l
}

func (g *Gotility) ZapErr(logger *zap.Logger, msg string, err error) {
	if err != nil {
		logger.Fatal(msg, zap.Error(err))
	}
}
