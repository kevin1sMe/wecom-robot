package config

import (
	"errors"
	"os"
)

type Config struct {
	Token          string
	EncodingAESKey string
	ReceiveID      string
	Port           string
}

func FromEnv() (*Config, error) {
	cfg := &Config{
		Token:          os.Getenv("WECOM_TOKEN"),
		EncodingAESKey: os.Getenv("WECOM_ENCODING_AES_KEY"),
		ReceiveID:      os.Getenv("WECOM_RECEIVE_ID"),
		Port:           os.Getenv("PORT"),
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.Token == "" || cfg.EncodingAESKey == "" {
		return nil, errors.New("必须设置 WECOM_TOKEN 与 WECOM_ENCODING_AES_KEY")
	}
	return cfg, nil
}
