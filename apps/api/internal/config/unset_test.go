package config

import "os"

func osUnsetenv(key string) error { return os.Unsetenv(key) }
