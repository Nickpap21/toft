package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var global *zap.Logger

// Init initializes the global logger. Call once at startup.
func Init(level string, production bool) {
	var cfg zap.Config

	if production {
		cfg = zap.NewProductionConfig()
	} else {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		lvl = zapcore.InfoLevel
	}
	cfg.Level = zap.NewAtomicLevelAt(lvl)

	l, err := cfg.Build()
	if err != nil {
		panic("failed to initialize logger: " + err.Error())
	}
	global = l
}

// Get returns the global logger, panicking if Init was not called.
func Get() *zap.Logger {
	if global == nil {
		panic("logger not initialized: call logger.Init() first")
	}
	return global
}

// Named returns a child logger with a given domain name, e.g. logger.Named("project").
func Named(domain string) *zap.Logger {
	return Get().Named(domain)
}

// Sync flushes any buffered log entries. Should be called with defer in main().
func Sync() {
	_ = Get().Sync()
}
