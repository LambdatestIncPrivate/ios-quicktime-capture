// Logging package for tunnel server

package lumber

import (
	"errors"
)

// LoggingConfig stores the config for the logger
// For some loggers there can only be one level across writers, for such the level of Console is picked by default
type LoggingConfig struct {
	EnableConsole     bool
	ConsoleJSONFormat bool
	ConsoleLevel      string
	EnableFile        bool
	FileJSONFormat    bool
	FileLevel         string
	FileLocation      string
}

// A global variable so that log functions can be directly accessed
var log Logger

// Fields Type to pass when we want to call WithFields for structured logging
type Fields map[string]interface{}

const (
	//Debug has verbose message
	Debug = "debug"
	//Info is default log level
	Info = "info"
	//Warn is for logging messages about possible issues
	Warn = "warn"
	//Error is for logging errors
	Error = "error"
	//Fatal is for logging fatal messages. The system shutsdown after logging the message.
	Fatal = "fatal"
)

const (
	InstanceZapLogger int = iota
	InstanceLogrusLogger
)

var (
	errInvalidLoggerInstance = errors.New("Invalid logger instance")
)

// Logger is our contract for the logger
type Logger interface {
	Debugf(format string, args ...interface{})

	Infof(format string, args ...interface{})

	Warnf(format string, args ...interface{})

	Errorf(format string, args ...interface{})

	Fatalf(format string, args ...interface{})

	Panicf(format string, args ...interface{})
	Log(args ...interface{}) error

	WithFields(keyValues Fields) Logger
}

// NewLogger returns an instance of logger
func NewLogger(config LoggingConfig, verbose bool, loggerInstance int) error {
	switch loggerInstance {
	case InstanceZapLogger:
		logger, err := newZapLogger(config, verbose)
		if err != nil {
			return err
		}
		log = logger
		return nil

	case InstanceLogrusLogger:
		logger, err := newLogrusLogger(config, verbose)
		if err != nil {
			return err
		}
		log = logger
		return nil

	default:
		return errInvalidLoggerInstance
	}
}

func Debugf(format string, args ...interface{}) {
	log.Debugf(format, args...)
}

func Infof(format string, args ...interface{}) {
	log.Infof(format, args...)
}

func Warnf(format string, args ...interface{}) {
	log.Warnf(format, args...)
}

func Errorf(format string, args ...interface{}) {
	log.Errorf(format, args...)
}

func Fatalf(format string, args ...interface{}) {
	log.Fatalf(format, args...)
}

func Panicf(format string, args ...interface{}) {
	log.Panicf(format, args...)
}

func Log(args ...interface{}) error {
	log.Debugf("Log ", args...)
	return nil
}

func GetLogger() Logger {
	return log
}

func WithFields(keyValues Fields) Logger {
	return log.WithFields(keyValues)
}
