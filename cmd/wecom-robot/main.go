package main

import (
    "fmt"
    "log"
    "net/http"
    "os"
    "path/filepath"

    "wecom-robot/internal/config"
    "wecom-robot/internal/server"
    "wecom-robot/internal/wecom"
)

func main() {
    cfg, err := config.FromEnv()
    if err != nil {
        log.Fatalf("配置错误: %v", err)
    }

    // Startup checks: trace/log dir and cache dir permissions to avoid silent failures
    // ReaderLogDir is always used for per-URL traces (if non-empty)
    if cfg.ReaderLogDir != "" {
        if ok, msg := ensureDirWritable(cfg.ReaderLogDir); ok {
            abs, _ := filepath.Abs(cfg.ReaderLogDir)
            log.Printf("[startup] trace dir OK: %s", abs)
        } else {
            log.Printf("[startup] trace dir disabled (%s)", msg)
            cfg.ReaderLogDir = ""
        }
    } else {
        log.Printf("[startup] trace dir disabled (empty)")
    }

    // ReaderCacheDir is only used when Redis is not configured
    if cfg.RedisAddr == "" {
        if cfg.ReaderCacheDir != "" {
            if ok, msg := ensureDirWritable(cfg.ReaderCacheDir); ok {
                abs, _ := filepath.Abs(cfg.ReaderCacheDir)
                log.Printf("[startup] file cache dir OK: %s", abs)
            } else {
                log.Printf("[startup] file cache disabled (%s)", msg)
                cfg.ReaderCacheDir = ""
            }
        } else {
            log.Printf("[startup] file cache disabled (empty)")
        }
    } else {
        log.Printf("[startup] Redis enabled, file cache disabled. REDIS_ADDR=%s", cfg.RedisAddr)
    }

	wc := wecom.NewWXBizMsgCrypt(cfg.Token, cfg.EncodingAESKey, cfg.ReceiveID, wecom.XmlType)

	mux := server.NewMux(cfg, wc)

    addr := ":" + cfg.Port
    log.Printf("WeCom 回调服务已启动, 监听 %s", addr)
    log.Printf("ReceiveID=%q", cfg.ReceiveID)
    if err := http.ListenAndServe(addr, mux); err != nil {
        log.Fatalf("服务异常退出: %v", err)
    }
}

// ensureDirWritable creates the directory if needed and verifies we can write a temp file.
// Returns (true, "") on success, or (false, reason) on failure.
func ensureDirWritable(dir string) (bool, string) {
    if dir == "" {
        return false, "empty path"
    }
    if err := os.MkdirAll(dir, 0o755); err != nil {
        return false, fmt.Sprintf("mkdir: %v", err)
    }
    f, err := os.CreateTemp(dir, ".permcheck-*")
    if err != nil {
        return false, fmt.Sprintf("create temp: %v", err)
    }
    name := f.Name()
    _, werr := f.WriteString("ok")
    cerr := f.Close()
    if werr != nil {
        _ = os.Remove(name)
        return false, fmt.Sprintf("write temp: %v", werr)
    }
    if cerr != nil {
        _ = os.Remove(name)
        return false, fmt.Sprintf("close temp: %v", cerr)
    }
    if err := os.Remove(name); err != nil {
        return false, fmt.Sprintf("cleanup temp: %v", err)
    }
    return true, ""
}
