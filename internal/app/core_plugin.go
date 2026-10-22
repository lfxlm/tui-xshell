package app

import (
	"context"
	"fmt"
	"runtime"

	"tui-ssh-workbench/internal/plugin"
)

type corePlugin struct{}

func (corePlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:         "system",
		Name:       "系统信息",
		Version:    "1.0.0",
		APIVersion: plugin.APIVersion,
	}
}

func (corePlugin) Register(registrar plugin.Registrar) error {
	return registrar.RegisterCommand("runtime", "显示当前 Go 运行时和平台信息", func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		return fmt.Sprintf("Go 版本：%s\n运行平台：%s/%s", runtime.Version(), runtime.GOOS, runtime.GOARCH), nil
	})
}
