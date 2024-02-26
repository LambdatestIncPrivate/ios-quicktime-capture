package lumber

import (
	"io"
	"os"
	"runtime"

	"github.com/sirupsen/logrus"
	lumberjack "gopkg.in/natefinch/lumberjack.v2"
)

type logrusLogEntry struct {
	entry *logrus.Entry
}

type logrusLogger struct {
	logger *logrus.Logger
}

func getFormatter(isJSON bool) logrus.Formatter {
	if isJSON {
		return &logrus.JSONFormatter{}
	}
	return &logrus.TextFormatter{
		FullTimestamp:          true,
		DisableLevelTruncation: true,
	}
}

func newLogrusLogger(config LoggingConfig, verbose bool) (Logger, error) {
	logLevel := config.ConsoleLevel
	if logLevel == "" {
		logLevel = config.FileLevel
	}
	// command line args take highest precedence
	if verbose {
		logLevel = "debug"
	}

	level, err := logrus.ParseLevel(logLevel)
	if err != nil {
		return nil, err
	}

	stdOutHandler := os.Stdout
	fileHandler := &lumberjack.Logger{
		Filename: config.FileLocation,
		MaxSize:  100,
		Compress: true,
		MaxAge:   28,
	}
	lLogger := &logrus.Logger{
		Out:       stdOutHandler,
		Formatter: getFormatter(config.ConsoleJSONFormat),
		Hooks:     make(logrus.LevelHooks),
		Level:     level,
	}

	if config.EnableConsole && config.EnableFile {
		lLogger.SetOutput(io.MultiWriter(stdOutHandler, fileHandler))
	} else {
		if config.EnableFile {
			lLogger.SetOutput(fileHandler)
			lLogger.SetFormatter(getFormatter(config.FileJSONFormat))
		}
	}

	return &logrusLogger{
		logger: lLogger,
	}, nil
}

func (l *logrusLogger) Debugf(format string, args ...interface{}) {
	pc, fn, line, _ := runtime.Caller(1)
	l.logger.WithFields(logrus.Fields{
		"function": runtime.FuncForPC(pc).Name(),
		"file":     fn,
		"line":     line,
	}).Debugf(format, args...)
}

func (l *logrusLogger) Infof(format string, args ...interface{}) {
	pc, fn, line, _ := runtime.Caller(1)
	l.logger.WithFields(logrus.Fields{
		"function": runtime.FuncForPC(pc).Name(),
		"file":     fn,
		"line":     line,
	}).Infof(format, args...)
}

func (l *logrusLogger) Warnf(format string, args ...interface{}) {
	l.logger.Warnf(format, args...)
}

func (l *logrusLogger) Errorf(format string, args ...interface{}) {
	pc, fn, line, _ := runtime.Caller(1)
	l.logger.WithFields(logrus.Fields{
		"function": runtime.FuncForPC(pc).Name(),
		"file":     fn,
		"line":     line,
	}).Errorf(format, args...)
}

func (l *logrusLogger) Fatalf(format string, args ...interface{}) {
	pc, fn, line, _ := runtime.Caller(1)
	l.logger.WithFields(logrus.Fields{
		"function": runtime.FuncForPC(pc).Name(),
		"file":     fn,
		"line":     line,
	}).Fatalf(format, args...)
}

func (l *logrusLogger) Panicf(format string, args ...interface{}) {
	pc, fn, line, _ := runtime.Caller(1)
	l.logger.WithFields(logrus.Fields{
		"function": runtime.FuncForPC(pc).Name(),
		"file":     fn,
		"line":     line,
	}).Fatalf(format, args...)
}

func (l *logrusLogger) Log(args ...interface{}) error {
	l.logger.Debugf("Log %+v", args...)
	return nil
}

func (l *logrusLogger) WithFields(fields Fields) Logger {
	return &logrusLogEntry{
		entry: l.logger.WithFields(convertToLogrusFields(fields)),
	}
}

func (l *logrusLogEntry) Debugf(format string, args ...interface{}) {
	l.entry.Debugf(format, args...)
}

func (l *logrusLogEntry) Infof(format string, args ...interface{}) {
	l.entry.Infof(format, args...)
}

func (l *logrusLogEntry) Warnf(format string, args ...interface{}) {
	l.entry.Warnf(format, args...)
}

func (l *logrusLogEntry) Errorf(format string, args ...interface{}) {
	l.entry.Errorf(format, args...)
}

func (l *logrusLogEntry) Fatalf(format string, args ...interface{}) {
	l.entry.Fatalf(format, args...)
}

func (l *logrusLogEntry) Panicf(format string, args ...interface{}) {
	l.entry.Fatalf(format, args...)
}

func (l *logrusLogEntry) Log(args ...interface{}) error {
	l.entry.Debugf("Log %+v", args...)
	return nil
}

func (l *logrusLogEntry) WithFields(fields Fields) Logger {
	return &logrusLogEntry{
		entry: l.entry.WithFields(convertToLogrusFields(fields)),
	}
}

func convertToLogrusFields(fields Fields) logrus.Fields {
	logrusFields := logrus.Fields{}
	for index, val := range fields {
		logrusFields[index] = val
	}
	return logrusFields
}
