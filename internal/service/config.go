// ================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// You can delete these comments if you wish manually maintain this interface file.
// ================================================================================

package service

import (
	"context"

	"tui-ssh-workbench/internal/model"

	"github.com/gogf/gf/v2/os/gcmd"
)

type (
	IConfigManager interface {
		ParseConfig(ctx context.Context, parser *gcmd.Parser) error
		Config() model.Config
	}
)

var (
	localConfigManager IConfigManager
)

func ConfigManager() IConfigManager {
	if localConfigManager == nil {
		panic("implement not found for interface IConfigManager, forgot register?")
	}
	return localConfigManager
}

func RegisterConfigManager(i IConfigManager) {
	localConfigManager = i
}
