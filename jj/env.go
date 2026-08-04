package main

import (
	"bufio"
	"log"
	"os"
	"strings"
)

// ==================== .env 环境变量加载 ====================
// config.json 中的密钥用 ${VAR} 占位，真实值放 .env(不入库)。
// 启动时先加载 .env，再在解析 config.json 前展开占位符。

// loadDotEnv 读取 .env(KEY=VALUE)，仅在环境未设置该变量时写入。
// .env 不存在时静默跳过(允许改用系统环境变量/systemd Environment=)。
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// 去掉包裹的引号
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, val)
		}
	}
}

// expandConfigEnv 将 config 内容中的 ${VAR} 用环境变量展开；缺失变量会告警。
func expandConfigEnv(data []byte) []byte {
	return []byte(os.Expand(string(data), func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Printf("[配置] 警告：环境变量 %s 未设置，占位符将展开为空", k)
		}
		return v
	}))
}
