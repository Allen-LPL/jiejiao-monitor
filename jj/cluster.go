package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ==================== 多点多活 / 故障转移 ====================
//
// 设计：每个节点有优先级(priority，数字越小优先级越高)。发送任何告警前，
// 节点先询问所有"更高优先级"的对等节点的 /health。只要有一个更高优先级节点
// 在线且具备对外发送能力(egress_ok)，本节点就抑制发送，交给它发；否则本节点发。
// 这样正常情况下只有主节点发送(无重复)，主节点宕机或对外发送能力丧失时，
// 备用节点自动接管。宁可重复不可漏——网络分区等极端情况下允许偶发重复。

// ClusterConfig 集群配置
type ClusterConfig struct {
	Enabled        bool         `json:"enabled"`
	NodeName       string       `json:"node_name"`
	Priority       int          `json:"priority"`         // 1=主节点(最高)，2/3...=备用
	Peers          []PeerConfig `json:"peers"`            // 其他节点
	EgressCheckURL string       `json:"egress_check_url"` // 自检对外发送能力的探测地址，默认 https://open.feishu.cn
	CheckTimeoutMs int          `json:"check_timeout_ms"` // 健康检查超时，默认 3000
}

// PeerConfig 对等节点
type PeerConfig struct {
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	HealthURL string `json:"health_url"` // 如 http://100.64.0.3:8082/health
}

// Cluster 集群运行时
type Cluster struct {
	cfg      ClusterConfig
	egressOK atomic.Bool

	egressFails atomic.Int32 // 连续探测失败次数，用于去抖

	// 对等节点健康状态短缓存，避免突发告警时频繁探测
	mu        sync.Mutex
	peerCache map[string]peerHealthCache
}

type peerHealthCache struct {
	healthy bool
	at      time.Time
}

// healthResponse /health 返回体
type healthResponse struct {
	Node     string `json:"node"`
	Priority int    `json:"priority"`
	Healthy  bool   `json:"healthy"`
	EgressOK bool   `json:"egress_ok"`
	Ts       int64  `json:"ts"`
}

func NewCluster(cfg ClusterConfig) *Cluster {
	if cfg.EgressCheckURL == "" {
		cfg.EgressCheckURL = "https://open.feishu.cn"
	}
	if cfg.CheckTimeoutMs <= 0 {
		cfg.CheckTimeoutMs = 8000
	}
	c := &Cluster{cfg: cfg, peerCache: make(map[string]peerHealthCache)}
	c.egressOK.Store(true) // 启动先乐观假设可发送，自检协程随后修正
	return c
}

func (c *Cluster) timeout() time.Duration {
	return time.Duration(c.cfg.CheckTimeoutMs) * time.Millisecond
}

// Start 启动对外发送能力自检协程，并注册 /health
func (c *Cluster) Start() {
	http.HandleFunc("/health", c.handleHealth)
	if !c.cfg.Enabled {
		return
	}
	log.Printf("[集群] 节点 %s 已启用，优先级 %d，对等节点 %d 个", c.cfg.NodeName, c.cfg.Priority, len(c.cfg.Peers))
	go func() {
		c.refreshEgress()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.refreshEgress()
		}
	}()
}

// refreshEgress 探测本节点能否对外发送。任意 HTTP 响应(含 404)即视为连通；
// 为避免单次网络抖动误判，连续 2 次失败才标记为不可发送，成功立即恢复。
func (c *Cluster) refreshEgress() {
	client := &http.Client{Timeout: c.timeout()}
	resp, err := client.Get(c.cfg.EgressCheckURL)
	if resp != nil {
		resp.Body.Close()
	}

	var ok bool
	if err == nil {
		c.egressFails.Store(0)
		ok = true
	} else {
		fails := c.egressFails.Add(1)
		ok = fails < 2 // 首次失败仍视为可用，连续 2 次才降级
	}

	prev := c.egressOK.Swap(ok)
	if prev != ok {
		log.Printf("[集群] 本节点对外发送能力变化: %v -> %v (最近错误: %v)", prev, ok, err)
	}
}

func (c *Cluster) handleHealth(w http.ResponseWriter, r *http.Request) {
	egress := c.egressOK.Load()
	resp := healthResponse{
		Node:     c.cfg.NodeName,
		Priority: c.cfg.Priority,
		Healthy:  egress,
		EgressOK: egress,
		Ts:       time.Now().Unix(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// ShouldSend 判断本节点当前是否应该发送告警
func (c *Cluster) ShouldSend(reason string) bool {
	if c == nil || !c.cfg.Enabled {
		return true // 单机模式，始终发送
	}

	// 检查是否有更高优先级(priority 更小)的节点在线且可发送
	for _, peer := range c.cfg.Peers {
		if peer.Priority >= c.cfg.Priority {
			continue // 只关心比自己优先级更高的节点
		}
		if c.peerHealthy(peer) {
			log.Printf("[集群] %s 由更高优先级节点 %s 负责发送，本节点抑制", reason, peer.Name)
			return false
		}
	}
	log.Printf("[集群] 本节点 %s 负责发送: %s", c.cfg.NodeName, reason)
	return true
}

// peerHealthy 查询对等节点是否在线且具备发送能力(带 5s 缓存)
func (c *Cluster) peerHealthy(peer PeerConfig) bool {
	c.mu.Lock()
	if cached, ok := c.peerCache[peer.Name]; ok && time.Since(cached.at) < 5*time.Second {
		c.mu.Unlock()
		return cached.healthy
	}
	c.mu.Unlock()

	healthy := false
	client := &http.Client{Timeout: c.timeout()}
	resp, err := client.Get(peer.HealthURL)
	if err == nil {
		defer resp.Body.Close()
		var h healthResponse
		if json.NewDecoder(resp.Body).Decode(&h) == nil {
			healthy = h.Healthy && h.EgressOK
		}
	}

	c.mu.Lock()
	c.peerCache[peer.Name] = peerHealthCache{healthy: healthy, at: time.Now()}
	c.mu.Unlock()
	return healthy
}
