package main

import (
	"os"
	"strconv"
	"time"
)

// Reference:
// https://www.gmarik.info/blog/2019/12-factor-golang-flag-package/

// LookupEnvOrString lookup ENV string with given key,
// or returns default value if not exists
func LookupEnvOrType[T int | bool | string | time.Duration](key string, defaultVal T) T {
	str, ok := os.LookupEnv(key)
	if !ok {
		return defaultVal
	}

	var val any
	var err error

	switch any(defaultVal).(type) {
	case int:
		val, err = strconv.Atoi(str)
	case bool:
		val, err = strconv.ParseBool(str)
	case time.Duration:
		val, err = time.ParseDuration(str)
	case string:
		val = str
	}

	if err == nil {
		return val.(T)
	}

	return defaultVal

}
