package main

import "errors"

func bOpenPTY([]string, uint16, uint16) (*bPTY, error) {
	return nil, errors.New("Web 终端服务运行在远端 Linux 容器中")
}
