package main

import (
	"log"
	"net/http"

	"wecom-robot/internal/config"
	"wecom-robot/internal/server"
	"wecom-robot/internal/wecom"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	wc, err := wecom.NewCrypto(cfg.Token, cfg.EncodingAESKey, cfg.ReceiveID)
	if err != nil {
		log.Fatalf("加解密初始化失败: %v", err)
	}

	mux := server.NewMux(wc)

	addr := ":" + cfg.Port
	log.Printf("WeCom 回调服务已启动, 监听 %s", addr)
	log.Printf("ReceiveID=%q", cfg.ReceiveID)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("服务异常退出: %v", err)
	}
}
