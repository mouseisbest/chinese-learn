package main

import (
	"fmt"
	"net"
)

// splitHostPort 拆出监听地址里的端口。
func splitHostPort(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", fmt.Errorf("解析监听地址 %q: %w", addr, err)
	}
	return host, port, nil
}

// lanIPs 返回本机所有非回环的 IPv4 地址。
//
// 用来提示家长「孩子平板上打开这个网址」——
// 少了这一步，用户往往不知道局域网要怎么连。
func lanIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			out = append(out, ip.String())
		}
	}
	return out
}
