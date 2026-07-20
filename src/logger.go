//go:build !js

package main

import (
	"log"
	"os"
)

// LogLevel represents the severity of log messages
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARN
	ERROR
)

var (
	currentLogLevel = DEBUG // Temporarily DEBUG for testing
	logger          = log.New(os.Stdout, "", log.Ldate|log.Ltime)
)

// LogDebug logs a debug message
func LogDebug(format string, args ...interface{}) {
	if currentLogLevel <= DEBUG {
		logger.Printf("[DEBUG] "+format, args...)
	}
}

// LogInfo logs an informational message
func LogInfo(format string, args ...interface{}) {
	if currentLogLevel <= INFO {
		logger.Printf("[INFO] "+format, args...)
	}
}

// LogWarn logs a warning message
func LogWarn(format string, args ...interface{}) {
	if currentLogLevel <= WARN {
		logger.Printf("[WARN] "+format, args...)
	}
}

// LogError logs an error message
func LogError(format string, args ...interface{}) {
	if currentLogLevel <= ERROR {
		logger.Printf("[ERROR] "+format, args...)
	}
}
