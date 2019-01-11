package main

import (
	"os"
	"strconv"
)

// etoi looks up an environment variable by name and, if defined, parses it as an integer, and
// returns the parsed integer. If the environment variable is undefined or cannot be parsed, it
// returns def.
//
// Valid integer strings are those supported by strconv.Atoi.
func etoi(name string, def int) int {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

// etoi looks up an environment variable by name and, if defined, parses it as a boolean, and
// returns the parsed boolean. If the environment variable is undefined or cannot be parsed, it
// returns def.
//
// Valid boolean strings are those supported by strconv.ParseBool.
func etob(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// etos looks up an environment variable and, if defined, returns its value. Otherwise, if the
// variable is not defined, it returns def.
func etos(name, def string) string {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	return v
}
