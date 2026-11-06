package logging

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

func Setup(env, filename string) (func() error, error) {
	var writer io.Writer = os.Stdout
	closeLog := func() error { return nil }
	switch env {
	case "development":
	case "", "production":
		if filename == "" {
			filename = "logs/wallet-kun.log"
		}
		if err := os.MkdirAll(filepath.Dir(filename), 0750); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		rotation := &lumberjack.Logger{Filename: filename, MaxSize: 10, MaxBackups: 5, MaxAge: 30, Compress: true}
		writer, closeLog = rotation, rotation.Close
	default:
		return nil, errors.New("APP_ENV must be production or development")
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(writer, nil)))
	return closeLog, nil
}
