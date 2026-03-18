package amqpbackend

import (
	"bytes"
	"log/slog"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}
