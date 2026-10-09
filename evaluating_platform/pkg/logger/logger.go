package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"
)

// Level 日志级别
type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

func (l Level) String() string {
	switch l {
	case DEBUG:
		return "DEBUG"
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Logger 结构化日志记录器
type Logger struct {
	level  Level
	logger *log.Logger
}

var std = New(INFO, os.Stdout)

// New 创建日志记录器
func New(level Level, out io.Writer) *Logger {
	return &Logger{
		level:  level,
		logger: log.New(out, "", 0),
	}
}

func (l *Logger) log(level Level, msg string, fields map[string]interface{}) {
	if level < l.level {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	entry := fmt.Sprintf("[%s] %s %s", level, ts, msg)
	for k, v := range fields {
		entry += fmt.Sprintf(" %s=%v", k, v)
	}
	l.logger.Println(entry)
}

func (l *Logger) Info(msg string, fields ...map[string]interface{}) {
	f := mergeFields(fields...)
	l.log(INFO, msg, f)
}

func (l *Logger) Warn(msg string, fields ...map[string]interface{}) {
	f := mergeFields(fields...)
	l.log(WARN, msg, f)
}

func (l *Logger) Error(msg string, fields ...map[string]interface{}) {
	f := mergeFields(fields...)
	l.log(ERROR, msg, f)
}

// 包级别便捷函数（使用全局 logger）
func Info(msg string, fields ...map[string]interface{})  { std.Info(msg, fields...) }
func Warn(msg string, fields ...map[string]interface{})  { std.Warn(msg, fields...) }
func Error(msg string, fields ...map[string]interface{}) { std.Error(msg, fields...) }

// SetLevel 设置全局日志级别
func SetLevel(level Level) { std.level = level }

func mergeFields(fields ...map[string]interface{}) map[string]interface{} {
	merged := map[string]interface{}{}
	for _, f := range fields {
		for k, v := range f {
			merged[k] = v
		}
	}
	return merged
}
