package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
	UploadDir   string
	StaticDir   string
	// 快速签收后游客可申诉的小时数（申诉期届满才释放押金）
	AppealWindowHours int
}

func Get() Config {
	c := Config{
		Port:              env("PORT", "8080"),
		DatabaseURL:       env("DATABASE_URL", "postgres://postgres@127.0.0.1:5433/skirental?sslmode=disable"),
		JWTSecret:         env("JWT_SECRET", "ski-rental-dev-secret"),
		UploadDir:         env("UPLOAD_DIR", "./data/uploads"),
		StaticDir:         env("STATIC_DIR", "../frontend/dist"),
		AppealWindowHours: envInt("APPEAL_WINDOW_HOURS", 48),
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
